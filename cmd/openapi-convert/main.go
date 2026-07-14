package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/getkin/kin-openapi/openapi2"
	"github.com/getkin/kin-openapi/openapi2conv"
	"github.com/getkin/kin-openapi/openapi3"
)

const (
	maximumOpenAPIDocumentBytes int64 = 16 * 1024 * 1024
	sourceSwaggerVersion              = "2.0"
	targetOpenAPIVersion              = "3.0.0"
)

func main() {
	if commandError := run(os.Args[1:]); commandError != nil {
		_, _ = fmt.Fprintf(os.Stderr, "openapi-convert: %v\n", commandError)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("expected 'convert <swagger.json> <openapi.json>' or 'validate <openapi.json>'")
	}

	switch arguments[0] {
	case "convert":
		if len(arguments) != 3 {
			return errors.New("convert expects an input and output path")
		}
		return convertFile(arguments[1], arguments[2])
	case "validate":
		if len(arguments) != 2 {
			return errors.New("validate expects one OpenAPI path")
		}
		return validateOpenAPI3File(arguments[1])
	default:
		return fmt.Errorf("unknown command %q", arguments[0])
	}
}

func convertFile(inputPath string, outputPath string) error {
	encodedSwagger, readError := readBoundedLocalFile(inputPath)
	if readError != nil {
		return fmt.Errorf("read Swagger input: %w", readError)
	}

	swaggerDocument, decodeError := decodeOpenAPI2Document(encodedSwagger)
	if decodeError != nil {
		return fmt.Errorf("decode Swagger input: %w", decodeError)
	}

	openAPIDocument, conversionError := openapi2conv.ToV3(swaggerDocument)
	if conversionError != nil {
		return fmt.Errorf("convert Swagger input: %w", conversionError)
	}
	openAPIDocument.OpenAPI = targetOpenAPIVersion
	removeConverterOnlyMetadata(openAPIDocument)
	if validationError := openAPIDocument.Validate(context.Background()); validationError != nil {
		return fmt.Errorf("validate converted OpenAPI document: %w", validationError)
	}

	encodedOpenAPI, encodeError := json.MarshalIndent(openAPIDocument, "", "  ")
	if encodeError != nil {
		return fmt.Errorf("encode converted OpenAPI document: %w", encodeError)
	}
	encodedOpenAPI = append(encodedOpenAPI, '\n')
	if int64(len(encodedOpenAPI)) > maximumOpenAPIDocumentBytes {
		return errors.New("converted OpenAPI document exceeds the output size limit")
	}
	if writeError := writeFileAtomically(outputPath, encodedOpenAPI); writeError != nil {
		return fmt.Errorf("write converted OpenAPI document: %w", writeError)
	}
	return nil
}

func validateOpenAPI3File(documentPath string) error {
	encodedOpenAPI, readError := readBoundedLocalFile(documentPath)
	if readError != nil {
		return fmt.Errorf("read OpenAPI document: %w", readError)
	}
	if syntaxError := validateStrictJSON(encodedOpenAPI); syntaxError != nil {
		return fmt.Errorf("decode OpenAPI document: %w", syntaxError)
	}
	if referenceError := validateLocalReferences(encodedOpenAPI); referenceError != nil {
		return fmt.Errorf("validate OpenAPI references: %w", referenceError)
	}

	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = false
	openAPIDocument, loadError := loader.LoadFromData(encodedOpenAPI)
	if loadError != nil {
		return fmt.Errorf("load OpenAPI document: %w", loadError)
	}
	if openAPIDocument.OpenAPI != targetOpenAPIVersion {
		return fmt.Errorf("OpenAPI version is %q, want %q", openAPIDocument.OpenAPI, targetOpenAPIVersion)
	}
	if validationError := openAPIDocument.Validate(loader.Context); validationError != nil {
		return fmt.Errorf("validate OpenAPI document: %w", validationError)
	}
	return nil
}

func decodeOpenAPI2Document(encodedSwagger []byte) (*openapi2.T, error) {
	if syntaxError := validateStrictJSON(encodedSwagger); syntaxError != nil {
		return nil, syntaxError
	}
	if referenceError := validateLocalReferences(encodedSwagger); referenceError != nil {
		return nil, referenceError
	}

	var swaggerDocument openapi2.T
	if decodeError := json.Unmarshal(encodedSwagger, &swaggerDocument); decodeError != nil {
		return nil, decodeError
	}
	if validationError := validateOpenAPI2Document(&swaggerDocument); validationError != nil {
		return nil, validationError
	}
	return &swaggerDocument, nil
}

func validateOpenAPI2Document(swaggerDocument *openapi2.T) error {
	if swaggerDocument.Swagger != sourceSwaggerVersion {
		return fmt.Errorf("Swagger version is %q, want %q", swaggerDocument.Swagger, sourceSwaggerVersion)
	}
	if infoError := swaggerDocument.Info.Validate(context.Background()); infoError != nil {
		return fmt.Errorf("validate info: %w", infoError)
	}
	if swaggerDocument.Paths == nil {
		return errors.New("paths is required")
	}
	if swaggerDocument.BasePath != "" && !strings.HasPrefix(swaggerDocument.BasePath, "/") {
		return errors.New("basePath must begin with a slash")
	}
	if strings.ContainsAny(swaggerDocument.BasePath, "?#") {
		return errors.New("basePath must not contain a query or fragment")
	}
	for _, scheme := range swaggerDocument.Schemes {
		switch scheme {
		case "http", "https", "ws", "wss":
		default:
			return fmt.Errorf("unsupported Swagger scheme %q", scheme)
		}
	}
	if extensionError := validateOpenAPI2Extensions(swaggerDocument); extensionError != nil {
		return extensionError
	}
	return nil
}

func validateStrictJSON(encodedDocument []byte) error {
	if !utf8.Valid(encodedDocument) {
		return errors.New("document is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(encodedDocument))
	decoder.UseNumber()
	if valueError := consumeJSONValue(decoder, "$"); valueError != nil {
		return valueError
	}
	if _, trailingError := decoder.Token(); !errors.Is(trailingError, io.EOF) {
		if trailingError == nil {
			return errors.New("document contains more than one JSON value")
		}
		return trailingError
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder, jsonPath string) error {
	jsonToken, tokenError := decoder.Token()
	if tokenError != nil {
		return tokenError
	}
	jsonDelimiter, isDelimiter := jsonToken.(json.Delim)
	if !isDelimiter {
		return nil
	}

	switch jsonDelimiter {
	case '{':
		propertyNames := make(map[string]struct{})
		for decoder.More() {
			propertyToken, propertyError := decoder.Token()
			if propertyError != nil {
				return propertyError
			}
			propertyName, isPropertyName := propertyToken.(string)
			if !isPropertyName {
				return fmt.Errorf("%s contains a non-string property name", jsonPath)
			}
			if _, duplicateProperty := propertyNames[propertyName]; duplicateProperty {
				return fmt.Errorf("%s contains duplicate property %q", jsonPath, propertyName)
			}
			propertyNames[propertyName] = struct{}{}
			if propertyError := consumeJSONValue(decoder, jsonPath+"."+propertyName); propertyError != nil {
				return propertyError
			}
		}
	case '[':
		arrayIndex := 0
		for decoder.More() {
			if elementError := consumeJSONValue(decoder, fmt.Sprintf("%s[%d]", jsonPath, arrayIndex)); elementError != nil {
				return elementError
			}
			arrayIndex++
		}
	default:
		return fmt.Errorf("%s contains unexpected delimiter %q", jsonPath, jsonDelimiter)
	}

	closingToken, closingError := decoder.Token()
	if closingError != nil {
		return closingError
	}
	expectedDelimiter := json.Delim('}')
	if jsonDelimiter == '[' {
		expectedDelimiter = ']'
	}
	if closingToken != expectedDelimiter {
		return fmt.Errorf("%s closes with unexpected delimiter %q", jsonPath, closingToken)
	}
	return nil
}

func validateLocalReferences(encodedDocument []byte) error {
	var documentValue any
	decoder := json.NewDecoder(bytes.NewReader(encodedDocument))
	decoder.UseNumber()
	if decodeError := decoder.Decode(&documentValue); decodeError != nil {
		return decodeError
	}
	return validateReferenceValue(documentValue, "$")
}

func validateReferenceValue(documentValue any, jsonPath string) error {
	switch typedValue := documentValue.(type) {
	case map[string]any:
		if referenceValue, hasReference := typedValue["$ref"]; hasReference {
			reference, isString := referenceValue.(string)
			if !isString || !strings.HasPrefix(reference, "#/") {
				return fmt.Errorf("%s.$ref must be a non-empty local document reference", jsonPath)
			}
			for propertyName := range typedValue {
				if propertyName != "$ref" && !strings.HasPrefix(propertyName, "x-") {
					return fmt.Errorf("%s reference has unsupported sibling %q", jsonPath, propertyName)
				}
			}
		}
		for propertyName, propertyValue := range typedValue {
			if referenceError := validateReferenceValue(propertyValue, jsonPath+"."+propertyName); referenceError != nil {
				return referenceError
			}
		}
	case []any:
		for arrayIndex, elementValue := range typedValue {
			if referenceError := validateReferenceValue(elementValue, fmt.Sprintf("%s[%d]", jsonPath, arrayIndex)); referenceError != nil {
				return referenceError
			}
		}
	}
	return nil
}

func validateOpenAPI2Extensions(swaggerDocument *openapi2.T) error {
	if extensionError := validateExtensionKeys("document", swaggerDocument.Extensions); extensionError != nil {
		return extensionError
	}
	if extensionError := validateInfoExtensions("info", &swaggerDocument.Info); extensionError != nil {
		return extensionError
	}
	if extensionError := validateExternalDocsExtensions("externalDocs", swaggerDocument.ExternalDocs); extensionError != nil {
		return extensionError
	}
	for tagIndex, tag := range swaggerDocument.Tags {
		if extensionError := validateTagExtensions(fmt.Sprintf("tags[%d]", tagIndex), tag); extensionError != nil {
			return extensionError
		}
	}
	for parameterName, parameter := range swaggerDocument.Parameters {
		if extensionError := validateParameterExtensions("parameters."+parameterName, parameter); extensionError != nil {
			return extensionError
		}
	}
	for responseName, response := range swaggerDocument.Responses {
		if extensionError := validateResponseExtensions("responses."+responseName, response); extensionError != nil {
			return extensionError
		}
	}
	for schemaName, schema := range swaggerDocument.Definitions {
		if extensionError := validateSchemaExtensions("definitions."+schemaName, schema); extensionError != nil {
			return extensionError
		}
	}
	for securityName, securityScheme := range swaggerDocument.SecurityDefinitions {
		if extensionError := validateSecuritySchemeExtensions("securityDefinitions."+securityName, securityScheme); extensionError != nil {
			return extensionError
		}
	}
	for path, pathItem := range swaggerDocument.Paths {
		if pathItem == nil {
			return fmt.Errorf("path %q is null", path)
		}
		pathLocation := "paths." + path
		if extensionError := validateExtensionKeys(pathLocation, pathItem.Extensions); extensionError != nil {
			return extensionError
		}
		for parameterIndex, parameter := range pathItem.Parameters {
			if extensionError := validateParameterExtensions(fmt.Sprintf("%s.parameters[%d]", pathLocation, parameterIndex), parameter); extensionError != nil {
				return extensionError
			}
		}
		for method, operation := range pathItem.Operations() {
			if extensionError := validateOperationExtensions(pathLocation+"."+strings.ToLower(method), operation); extensionError != nil {
				return extensionError
			}
		}
	}
	return nil
}

func validateInfoExtensions(location string, info *openapi3.Info) error {
	if extensionError := validateExtensionKeys(location, info.Extensions); extensionError != nil {
		return extensionError
	}
	if info.Contact != nil {
		if extensionError := validateExtensionKeys(location+".contact", info.Contact.Extensions); extensionError != nil {
			return extensionError
		}
	}
	if info.License != nil {
		if extensionError := validateExtensionKeys(location+".license", info.License.Extensions); extensionError != nil {
			return extensionError
		}
	}
	return nil
}

func validateTagExtensions(location string, tag *openapi3.Tag) error {
	if tag == nil {
		return fmt.Errorf("%s is null", location)
	}
	if extensionError := validateExtensionKeys(location, tag.Extensions); extensionError != nil {
		return extensionError
	}
	return validateExternalDocsExtensions(location+".externalDocs", tag.ExternalDocs)
}

func validateExternalDocsExtensions(location string, externalDocs *openapi3.ExternalDocs) error {
	if externalDocs == nil {
		return nil
	}
	return validateExtensionKeys(location, externalDocs.Extensions)
}

func validateOperationExtensions(location string, operation *openapi2.Operation) error {
	if operation == nil {
		return fmt.Errorf("%s is null", location)
	}
	if extensionError := validateExtensionKeys(location, operation.Extensions); extensionError != nil {
		return extensionError
	}
	if extensionError := validateExternalDocsExtensions(location+".externalDocs", operation.ExternalDocs); extensionError != nil {
		return extensionError
	}
	for parameterIndex, parameter := range operation.Parameters {
		if extensionError := validateParameterExtensions(fmt.Sprintf("%s.parameters[%d]", location, parameterIndex), parameter); extensionError != nil {
			return extensionError
		}
	}
	for responseStatus, response := range operation.Responses {
		if extensionError := validateResponseExtensions(location+".responses."+responseStatus, response); extensionError != nil {
			return extensionError
		}
	}
	return nil
}

func validateParameterExtensions(location string, parameter *openapi2.Parameter) error {
	if parameter == nil {
		return fmt.Errorf("%s is null", location)
	}
	if extensionError := validateExtensionKeys(location, parameter.Extensions); extensionError != nil {
		return extensionError
	}
	if extensionError := validateSchemaExtensions(location+".schema", parameter.Schema); extensionError != nil {
		return extensionError
	}
	return validateSchemaExtensions(location+".items", parameter.Items)
}

func validateResponseExtensions(location string, response *openapi2.Response) error {
	if response == nil {
		return fmt.Errorf("%s is null", location)
	}
	if extensionError := validateExtensionKeys(location, response.Extensions); extensionError != nil {
		return extensionError
	}
	if extensionError := validateSchemaExtensions(location+".schema", response.Schema); extensionError != nil {
		return extensionError
	}
	for headerName, header := range response.Headers {
		if header == nil {
			return fmt.Errorf("%s.headers.%s is null", location, headerName)
		}
		if extensionError := validateParameterExtensions(location+".headers."+headerName, &header.Parameter); extensionError != nil {
			return extensionError
		}
	}
	return nil
}

func validateSecuritySchemeExtensions(location string, securityScheme *openapi2.SecurityScheme) error {
	if securityScheme == nil {
		return fmt.Errorf("%s is null", location)
	}
	if extensionError := validateExtensionKeys(location, securityScheme.Extensions); extensionError != nil {
		return extensionError
	}
	for tagIndex, tag := range securityScheme.Tags {
		if extensionError := validateTagExtensions(fmt.Sprintf("%s.tags[%d]", location, tagIndex), tag); extensionError != nil {
			return extensionError
		}
	}
	return nil
}

func validateSchemaExtensions(location string, schemaReference *openapi2.SchemaRef) error {
	if schemaReference == nil {
		return nil
	}
	if extensionError := validateExtensionKeys(location, schemaReference.Extensions); extensionError != nil {
		return extensionError
	}
	if schemaReference.Value == nil {
		return nil
	}

	schema := schemaReference.Value
	if extensionError := validateExtensionKeys(location, schema.Extensions); extensionError != nil {
		return extensionError
	}
	if extensionError := validateExternalDocsExtensions(location+".externalDocs", schema.ExternalDocs); extensionError != nil {
		return extensionError
	}
	if schema.XML != nil {
		if extensionError := validateExtensionKeys(location+".xml", schema.XML.Extensions); extensionError != nil {
			return extensionError
		}
	}
	for allOfIndex, allOfSchema := range schema.AllOf {
		if extensionError := validateSchemaExtensions(fmt.Sprintf("%s.allOf[%d]", location, allOfIndex), allOfSchema); extensionError != nil {
			return extensionError
		}
	}
	if extensionError := validateSchemaExtensions(location+".not", schema.Not); extensionError != nil {
		return extensionError
	}
	if extensionError := validateSchemaExtensions(location+".items", schema.Items); extensionError != nil {
		return extensionError
	}
	for propertyName, propertySchema := range schema.Properties {
		if extensionError := validateSchemaExtensions(location+".properties."+propertyName, propertySchema); extensionError != nil {
			return extensionError
		}
	}
	return nil
}

func validateExtensionKeys(location string, extensions map[string]any) error {
	for extensionName := range extensions {
		if !strings.HasPrefix(extensionName, "x-") {
			return fmt.Errorf("%s contains unknown property %q", location, extensionName)
		}
	}
	return nil
}

func removeConverterOnlyMetadata(openAPIDocument *openapi3.T) {
	if openAPIDocument.Paths != nil {
		for _, pathItem := range openAPIDocument.Paths.Map() {
			for _, operation := range pathItem.Operations() {
				removeOriginalParameterName(operation.RequestBody)
			}
		}
	}
	if openAPIDocument.Components != nil {
		for _, requestBody := range openAPIDocument.Components.RequestBodies {
			removeOriginalParameterName(requestBody)
		}
	}
}

func removeOriginalParameterName(requestBody *openapi3.RequestBodyRef) {
	if requestBody == nil || requestBody.Value == nil {
		return
	}
	delete(requestBody.Value.Extensions, "x-originalParamName")
	if len(requestBody.Value.Extensions) == 0 {
		requestBody.Value.Extensions = nil
	}
}

func readBoundedLocalFile(documentPath string) ([]byte, error) {
	fileInfo, statError := os.Lstat(documentPath)
	if statError != nil {
		return nil, statError
	}
	if !fileInfo.Mode().IsRegular() {
		return nil, errors.New("path must identify a regular local file")
	}
	if fileInfo.Size() > maximumOpenAPIDocumentBytes {
		return nil, errors.New("document exceeds the input size limit")
	}

	documentFile, openError := os.Open(documentPath)
	if openError != nil {
		return nil, openError
	}
	defer documentFile.Close()
	encodedDocument, readError := io.ReadAll(io.LimitReader(documentFile, maximumOpenAPIDocumentBytes+1))
	if readError != nil {
		return nil, readError
	}
	if int64(len(encodedDocument)) > maximumOpenAPIDocumentBytes {
		return nil, errors.New("document exceeds the input size limit")
	}
	return encodedDocument, nil
}

func writeFileAtomically(documentPath string, encodedDocument []byte) error {
	outputMode := os.FileMode(0o644)
	if existingInfo, statError := os.Lstat(documentPath); statError == nil {
		if !existingInfo.Mode().IsRegular() {
			return errors.New("output path must identify a regular local file")
		}
		outputMode = existingInfo.Mode().Perm()
	} else if !errors.Is(statError, os.ErrNotExist) {
		return statError
	}

	outputDirectory := filepath.Dir(documentPath)
	temporaryFile, createError := os.CreateTemp(outputDirectory, ".openapi-convert-*")
	if createError != nil {
		return createError
	}
	temporaryPath := temporaryFile.Name()
	defer os.Remove(temporaryPath)

	if chmodError := temporaryFile.Chmod(outputMode); chmodError != nil {
		_ = temporaryFile.Close()
		return chmodError
	}
	if _, writeError := temporaryFile.Write(encodedDocument); writeError != nil {
		_ = temporaryFile.Close()
		return writeError
	}
	if syncError := temporaryFile.Sync(); syncError != nil {
		_ = temporaryFile.Close()
		return syncError
	}
	if closeError := temporaryFile.Close(); closeError != nil {
		return closeError
	}
	if renameError := os.Rename(temporaryPath, documentPath); renameError != nil {
		return renameError
	}
	return nil
}
