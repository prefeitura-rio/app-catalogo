package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/prefeitura-rio/app-catalogo/internal/api/middleware"
	"github.com/prefeitura-rio/app-catalogo/internal/models"
	"github.com/prefeitura-rio/app-catalogo/internal/services"
)

const maximumSearchRequestBodyBytes = 16 << 10

var errUnsupportedSearchMediaType = errors.New("Content-Type deve ser application/json")

type SearchHandler struct {
	searchSvc *services.SearchService
}

func NewSearchHandler(searchSvc *services.SearchService) *SearchHandler {
	return &SearchHandler{searchSvc: searchSvc}
}

// Search godoc
// @Summary      Busca no catálogo
// @Description  Busca por serviços, cursos, vagas e oportunidades MEI. Suporta sintaxe websearch_to_tsquery: aspas para frase exata, - para exclusão, OR para alternativas.
// @Tags         busca
// @Produce      json
// @Param        q                  query  string    false  "Texto livre de busca"  maxlength(256)
// @Param        types              query  []string  false  "Tipos: service, course, job, mei_opportunity"  collectionFormat(multi)  Enums(service,course,job,mei_opportunity)
// @Param        page               query  int       false  "Página (default: 1)"  minimum(1)
// @Param        per_page           query  int       false  "Itens por página, máximo 100 (default: 10)"  minimum(1)  maximum(100)
// @Param        modalidade         query  string    false  "Modalidade: presencial, digital, hibrido"  Enums(presencial,digital,hibrido)
// @Param        bairro             query  string    false  "Bairro do Rio de Janeiro"  maxlength(100)
// @Param        orgao              query  string    false  "Órgão ou secretaria responsável"  maxlength(100)
// @Param        turno              query  string    false  "[course] Turno: matutino, vespertino, noturno"  Enums(matutino,vespertino,noturno)
// @Param        regime_contratacao query  string    false  "[job] Regime: clt, pj, temporario"  Enums(clt,pj,temporario)
// @Param        modelo_trabalho    query  string    false  "[job] Modelo: presencial, remoto, hibrido"  Enums(presencial,remoto,hibrido)
// @Param        pcd                query  bool      false  "[job] true/false filtra vagas PCD; omitido não filtra"
// @Param        canal_atendimento  query  string    false  "[service] Canal: presencial, digital, telefone"  Enums(presencial,digital,telefone)
// @Param        tema               query  string    false  "[service] Tema do serviço"  maxlength(100)
// @Param        segmento           query  string    false  "[mei_opportunity] Segmento do negócio"  maxlength(100)
// @Success      200                {object}  models.SearchResponse
// @Failure      400                {object}  map[string]string
// @Failure      429                {object}  map[string]string
// @Failure      500                {object}  map[string]string
// @Failure      504                {object}  map[string]string
// @Router       /api/v1/search [get]
// @Router       /api/public/search [get]
func (h *SearchHandler) Search(c *gin.Context) {
	searchRequest, searchRequestError := parseSearchQuery(c)
	h.executeSearch(c, searchRequest, searchRequestError)
}

// SearchJSON godoc
// @Summary      Busca no catálogo sem consulta na URL
// @Description  Executa o mesmo pipeline da busca GET usando um corpo JSON estritamente validado.
// @Tags         busca
// @Accept       json
// @Produce      json
// @Param        request  body  models.SearchRequestBody  true  "Parâmetros da busca"
// @Success      200      {object}  models.SearchResponse
// @Failure      400      {object}  map[string]string
// @Failure      413      {object}  map[string]string
// @Failure      415      {object}  map[string]string
// @Failure      429      {object}  map[string]string
// @Failure      500      {object}  map[string]string
// @Failure      504      {object}  map[string]string
// @Router       /api/v1/search [post]
// @Router       /api/public/search [post]
func (h *SearchHandler) SearchJSON(c *gin.Context) {
	searchRequest, searchRequestError := parseSearchJSON(c)
	h.executeSearch(c, searchRequest, searchRequestError)
}

func (h *SearchHandler) executeSearch(
	c *gin.Context,
	searchRequest models.SearchRequest,
	searchRequestError error,
) {
	if searchRequestError != nil {
		status := http.StatusBadRequest
		var maximumBytesError *http.MaxBytesError
		if errors.Is(searchRequestError, errUnsupportedSearchMediaType) {
			status = http.StatusUnsupportedMediaType
		} else if errors.As(searchRequestError, &maximumBytesError) {
			status = http.StatusRequestEntityTooLarge
		}
		c.JSON(status, gin.H{
			"error":  searchRequestError.Error(),
			"log_id": c.GetString("request_id"),
		})
		return
	}

	searchResponse, searchError := h.searchSvc.Search(c.Request.Context(), &searchRequest)
	if searchError != nil {
		requestID := c.GetString("request_id")
		if errors.Is(searchError, context.DeadlineExceeded) || errors.Is(searchError, context.Canceled) {
			log.Warn().Err(searchError).Str("request_id", requestID).Msg("search handler: search deadline exceeded")
			c.JSON(http.StatusGatewayTimeout, gin.H{
				"error":  "tempo limite da busca excedido",
				"log_id": requestID,
			})
			return
		}
		log.Error().Err(searchError).Str("request_id", requestID).Msg("search handler: falha na busca")
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":  "falha na busca",
			"log_id": requestID,
		})
		return
	}

	searchResponse.SearchID = c.GetString(middleware.SearchIDKey)
	if searchResponse.SearchID == "" {
		searchResponse.SearchID = c.GetString("request_id")
	}
	encodedResponse, encodeError := json.Marshal(searchResponse)
	if encodeError != nil || len(encodedResponse) > models.MaximumPublicSearchResponseBytes {
		requestID := c.GetString("request_id")
		log.Error().
			Str("request_id", requestID).
			Bool("encode_failed", encodeError != nil).
			Msg("search handler: response exceeded the safe public contract")
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":  "falha na busca",
			"log_id": requestID,
		})
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", encodedResponse)
}

func parseSearchJSON(c *gin.Context) (models.SearchRequest, error) {
	contentTypes := c.Request.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		return models.SearchRequest{}, errUnsupportedSearchMediaType
	}
	mediaType, _, mediaTypeError := mime.ParseMediaType(contentTypes[0])
	if mediaTypeError != nil || !strings.EqualFold(mediaType, "application/json") {
		return models.SearchRequest{}, errUnsupportedSearchMediaType
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maximumSearchRequestBodyBytes)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()

	var requestBody models.SearchRequestBody
	if decodeError := decoder.Decode(&requestBody); decodeError != nil {
		return models.SearchRequest{}, fmt.Errorf("corpo JSON da busca inválido: %w", decodeError)
	}
	if trailingJSONError := decoder.Decode(&struct{}{}); !errors.Is(trailingJSONError, io.EOF) {
		return models.SearchRequest{}, errors.New("corpo JSON da busca deve conter um único objeto")
	}
	if requestBody.Page != nil && *requestBody.Page < models.DefaultSearchPage {
		return models.SearchRequest{}, fmt.Errorf("page deve ser maior ou igual a %d", models.DefaultSearchPage)
	}
	if requestBody.PerPage != nil && (*requestBody.PerPage < 1 || *requestBody.PerPage > models.MaxSearchPerPage) {
		return models.SearchRequest{}, fmt.Errorf("per_page deve estar entre 1 e %d", models.MaxSearchPerPage)
	}

	searchRequest := requestBody.ToSearchRequest()
	searchRequest.Normalize()
	if validationError := searchRequest.Validate(); validationError != nil {
		return models.SearchRequest{}, validationError
	}
	return searchRequest, nil
}

func parseSearchQuery(c *gin.Context) (models.SearchRequest, error) {
	page, err := queryInt(c, "page", models.DefaultSearchPage)
	if err != nil {
		return models.SearchRequest{}, err
	}
	perPage, err := queryInt(c, "per_page", models.DefaultSearchPerPage)
	if err != nil {
		return models.SearchRequest{}, err
	}
	gratuito, err := queryBool(c, "gratuito")
	if err != nil {
		return models.SearchRequest{}, err
	}
	pcd, err := queryBool(c, "pcd")
	if err != nil {
		return models.SearchRequest{}, err
	}

	req := models.SearchRequest{
		Q:       c.Query("q"),
		Page:    page,
		PerPage: perPage,
	}
	if utf8.RuneCountInString(req.Q) > models.MaxSearchQueryRunes {
		return models.SearchRequest{}, fmt.Errorf("q excede o tamanho máximo permitido")
	}

	for _, raw := range c.QueryArray("types") {
		for _, rawItemType := range strings.Split(raw, ",") {
			if rawItemType = strings.TrimSpace(rawItemType); rawItemType != "" {
				req.Types = append(req.Types, models.ItemType(rawItemType))
			}
		}
	}

	req.Filters = models.SearchFilters{
		Modalidade:        c.Query("modalidade"),
		Bairro:            c.Query("bairro"),
		Orgao:             c.Query("orgao"),
		Gratuito:          gratuito,
		Turno:             c.Query("turno"),
		RegimeContratacao: c.Query("regime_contratacao"),
		ModeloTrabalho:    c.Query("modelo_trabalho"),
		PCD:               pcd,
		FaixaSalarial:     c.Query("faixa_salarial"),
		CanalAtendimento:  c.Query("canal_atendimento"),
		Tema:              c.Query("tema"),
		Segmento:          c.Query("segmento"),
	}

	req.Normalize()
	if err := req.Validate(); err != nil {
		return models.SearchRequest{}, err
	}
	return req, nil
}

func queryInt(c *gin.Context, key string, defaultValue int) (int, error) {
	rawValue, present := c.GetQuery(key)
	if !present {
		return defaultValue, nil
	}

	parsedValue, err := strconv.Atoi(strings.TrimSpace(rawValue))
	if err != nil {
		return 0, fmt.Errorf("%s deve ser um número inteiro válido", key)
	}
	if key == "page" && parsedValue < models.DefaultSearchPage {
		return 0, fmt.Errorf("page deve ser maior ou igual a %d", models.DefaultSearchPage)
	}
	if key == "per_page" && (parsedValue < 1 || parsedValue > models.MaxSearchPerPage) {
		return 0, fmt.Errorf("per_page deve estar entre 1 e %d", models.MaxSearchPerPage)
	}
	return parsedValue, nil
}

// queryBool distinguishes an absent filter from an explicitly false filter.
func queryBool(c *gin.Context, key string) (*bool, error) {
	rawValue, present := c.GetQuery(key)
	if !present {
		return nil, nil
	}

	var parsedValue bool
	switch strings.ToLower(strings.TrimSpace(rawValue)) {
	case "true":
		parsedValue = true
	case "false":
		parsedValue = false
	default:
		return nil, fmt.Errorf("%s deve ser true ou false", key)
	}
	return &parsedValue, nil
}
