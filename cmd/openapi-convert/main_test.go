package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validSwaggerDocument = `{
  "swagger": "2.0",
  "info": {"title": "example", "version": "1.0"},
  "basePath": "/",
  "paths": {
    "/health": {
      "get": {
        "responses": {
          "200": {
            "description": "OK",
            "schema": {"type": "object"}
          }
        }
      }
    }
  }
}`

func TestConvertFileWritesValidatedCompatibleOpenAPIAtomically(t *testing.T) {
	t.Parallel()
	temporaryDirectory := t.TempDir()
	inputPath := filepath.Join(temporaryDirectory, "swagger.json")
	outputPath := filepath.Join(temporaryDirectory, "openapi.json")
	writeTestFile(t, inputPath, validSwaggerDocument)

	if conversionError := convertFile(inputPath, outputPath); conversionError != nil {
		t.Fatalf("convert file: %v", conversionError)
	}
	if validationError := validateOpenAPI3File(outputPath); validationError != nil {
		t.Fatalf("validate converted file: %v", validationError)
	}

	encodedDocument, readError := os.ReadFile(outputPath)
	if readError != nil {
		t.Fatalf("read converted file: %v", readError)
	}
	var document map[string]any
	if decodeError := json.Unmarshal(encodedDocument, &document); decodeError != nil {
		t.Fatalf("decode converted file: %v", decodeError)
	}
	if document["openapi"] != targetOpenAPIVersion {
		t.Fatalf("OpenAPI version = %v, want %s", document["openapi"], targetOpenAPIVersion)
	}
	if fileInfo, statError := os.Stat(outputPath); statError != nil {
		t.Fatalf("stat converted file: %v", statError)
	} else if fileInfo.Mode().Perm() != 0o644 {
		t.Fatalf("converted mode = %o, want 644", fileInfo.Mode().Perm())
	}
}

func TestConvertFileRejectsUnknownSwaggerPropertyWithoutReplacingOutput(t *testing.T) {
	t.Parallel()
	temporaryDirectory := t.TempDir()
	inputPath := filepath.Join(temporaryDirectory, "swagger.json")
	outputPath := filepath.Join(temporaryDirectory, "openapi.json")
	writeTestFile(t, inputPath, strings.Replace(validSwaggerDocument, `"basePath": "/"`, `"basePath": "/", "unexpected": true`, 1))
	writeTestFile(t, outputPath, "existing output")

	conversionError := convertFile(inputPath, outputPath)
	if conversionError == nil || !strings.Contains(conversionError.Error(), "unknown property") {
		t.Fatalf("conversion error = %v, want unknown property", conversionError)
	}
	encodedOutput, readError := os.ReadFile(outputPath)
	if readError != nil {
		t.Fatalf("read preserved output: %v", readError)
	}
	if string(encodedOutput) != "existing output" {
		t.Fatalf("output was replaced: %q", encodedOutput)
	}
}

func TestConvertFileRejectsDuplicateJSONProperty(t *testing.T) {
	t.Parallel()
	temporaryDirectory := t.TempDir()
	inputPath := filepath.Join(temporaryDirectory, "swagger.json")
	outputPath := filepath.Join(temporaryDirectory, "openapi.json")
	writeTestFile(t, inputPath, strings.Replace(validSwaggerDocument, `"swagger": "2.0"`, `"swagger": "2.0", "swagger": "2.0"`, 1))

	conversionError := convertFile(inputPath, outputPath)
	if conversionError == nil || !strings.Contains(conversionError.Error(), "duplicate property") {
		t.Fatalf("conversion error = %v, want duplicate property", conversionError)
	}
}

func TestConvertFileRejectsUnknownNestedSwaggerProperty(t *testing.T) {
	t.Parallel()
	temporaryDirectory := t.TempDir()
	inputPath := filepath.Join(temporaryDirectory, "swagger.json")
	outputPath := filepath.Join(temporaryDirectory, "openapi.json")
	unknownOperationDocument := strings.Replace(
		validSwaggerDocument,
		`"responses": {`,
		`"unexpected": true, "responses": {`,
		1,
	)
	writeTestFile(t, inputPath, unknownOperationDocument)

	conversionError := convertFile(inputPath, outputPath)
	if conversionError == nil || !strings.Contains(conversionError.Error(), "unknown property") {
		t.Fatalf("conversion error = %v, want nested unknown property", conversionError)
	}
}

func TestConvertFileRejectsTrailingJSONValue(t *testing.T) {
	t.Parallel()
	temporaryDirectory := t.TempDir()
	inputPath := filepath.Join(temporaryDirectory, "swagger.json")
	outputPath := filepath.Join(temporaryDirectory, "openapi.json")
	writeTestFile(t, inputPath, validSwaggerDocument+" true")

	conversionError := convertFile(inputPath, outputPath)
	if conversionError == nil || !strings.Contains(conversionError.Error(), "more than one JSON value") {
		t.Fatalf("conversion error = %v, want trailing JSON error", conversionError)
	}
}

func TestConvertFileRejectsExternalReference(t *testing.T) {
	t.Parallel()
	temporaryDirectory := t.TempDir()
	inputPath := filepath.Join(temporaryDirectory, "swagger.json")
	outputPath := filepath.Join(temporaryDirectory, "openapi.json")
	externalReferenceDocument := strings.Replace(
		validSwaggerDocument,
		`{"type": "object"}`,
		`{"$ref": "https://example.invalid/schema.json"}`,
		1,
	)
	writeTestFile(t, inputPath, externalReferenceDocument)

	conversionError := convertFile(inputPath, outputPath)
	if conversionError == nil || !strings.Contains(conversionError.Error(), "local document reference") {
		t.Fatalf("conversion error = %v, want local reference error", conversionError)
	}
}

func TestValidateOpenAPI3FileRejectsUnexpectedVersion(t *testing.T) {
	t.Parallel()
	documentPath := filepath.Join(t.TempDir(), "openapi.json")
	writeTestFile(t, documentPath, `{"openapi":"3.0.3","info":{"title":"example","version":"1"},"paths":{}}`)

	validationError := validateOpenAPI3File(documentPath)
	if validationError == nil || !strings.Contains(validationError.Error(), "want \"3.0.0\"") {
		t.Fatalf("validation error = %v, want version mismatch", validationError)
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	t.Parallel()
	commandError := run([]string{"unknown"})
	if commandError == nil || !strings.Contains(commandError.Error(), "unknown command") {
		t.Fatalf("command error = %v, want unknown command", commandError)
	}
}

func writeTestFile(t *testing.T, documentPath string, documentContent string) {
	t.Helper()
	if writeError := os.WriteFile(documentPath, []byte(documentContent), 0o644); writeError != nil {
		t.Fatalf("write test file: %v", writeError)
	}
}
