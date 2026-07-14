# Keep the generated OpenAPI search contract deterministic where Swagger 2.0
# annotations cannot express the final OpenAPI 3.0 shape.

def search_paths:
  ["/api/v1/search", "/api/public/search"];

def bearer_security:
  [{"BearerAuth": []}];

def bounded_non_blank_text($maximum_length):
  .minLength = 1
  | .maxLength = $maximum_length
  | .pattern = "\\S";

def bounded_serialized_text($maximum_length):
  .minLength = 1
  | .maxLength = $maximum_length;

def bounded_catalog_http_url:
  .format = "uri"
  | .minLength = 1
  | .maxLength = 2048
  | .pattern = "^[Hh][Tt][Tt][Pp][Ss]?://[^/?#@\\s]+($|[/?#]($|.*\\S$))";

def bounded_catalog_string_array($minimum_items):
  .minItems = $minimum_items
  | .maxItems = 50
  | .items.minLength = 1
  | .items.maxLength = 500
  | .items.pattern = "\\S";

def target_audience_schema:
  {
    "type": "object",
    "additionalProperties": false,
    "x-max-json-bytes": 16384,
    "properties": {
      "escolaridade": {
        "type": "array",
        "nullable": true,
        "maxItems": 50,
        "items": {
          "type": "string",
          "minLength": 1,
          "maxLength": 500,
          "pattern": "\\S"
        }
      },
      "renda": {
        "type": "string",
        "nullable": true,
        "maxLength": 500
      },
      "deficiencia": {
        "type": "array",
        "nullable": true,
        "maxItems": 50,
        "items": {
          "type": "string",
          "minLength": 1,
          "maxLength": 500,
          "pattern": "\\S"
        }
      },
      "etnia": {
        "type": "array",
        "nullable": true,
        "maxItems": 50,
        "items": {
          "type": "string",
          "minLength": 1,
          "maxLength": 500,
          "pattern": "\\S"
        }
      },
      "faixa_etaria": {
        "type": "array",
        "nullable": true,
        "maxItems": 50,
        "items": {
          "type": "string",
          "minLength": 1,
          "maxLength": 500,
          "pattern": "\\S"
        }
      },
      "genero": {
        "type": "array",
        "nullable": true,
        "maxItems": 50,
        "items": {
          "type": "string",
          "minLength": 1,
          "maxLength": 500,
          "pattern": "\\S"
        }
      },
      "pcd": {
        "type": "boolean",
        "nullable": true
      }
    }
  };

def catalog_source_data_schema:
  {
    "type": "object",
    "additionalProperties": true,
    "x-max-json-bytes": 65536,
    "properties": {
      "canonical_id": {"type": "string", "nullable": true, "maxLength": 500},
      "id": {"type": "string", "nullable": true, "maxLength": 500},
      "slug": {"type": "string", "nullable": true, "maxLength": 500},
      "tema_especifico": {"type": "string", "nullable": true, "maxLength": 500},
      "tema_geral": {"type": "string", "nullable": true, "maxLength": 500},
      "_catalog_object_type": {
        "type": "string",
        "minLength": 1,
        "maxLength": 100,
        "pattern": "\\S"
      }
    }
  };

def search_metadata_schema:
  {
    "type": "object",
    "additionalProperties": false,
    "properties": {
      "id": {"type": "string", "maxLength": 500},
      "slug": {"type": "string", "maxLength": 500},
      "tema_especifico": {"type": "string", "maxLength": 500},
      "tema_geral": {"type": "string", "maxLength": 500}
    }
  };

def require_generated_search_contract:
  . as $document
  | if (
      all(search_paths[];
        . as $search_path
        | (($document.paths[$search_path].get | type) == "object")
          and (($document.paths[$search_path].post | type) == "object")
          and (
            (
              $document.paths[$search_path].post.requestBody.content["application/json"].schema["$ref"]
              == "#/components/schemas/models.SearchRequestBody"
            )
            or (
              $document.paths[$search_path].post.requestBody["$ref"]
              == "#/components/requestBodies/models.SearchRequestBody"
            )
          )
          and (
            $document.paths[$search_path].post.responses["200"].content["application/json"].schema["$ref"]
            == "#/components/schemas/models.SearchResponse"
          )
          and (
            ($document.paths[$search_path].post.responses | keys | sort)
            == ["200", "400", "413", "415", "429", "500", "504"]
          )
          and (
            ($document.paths[$search_path].get.responses | keys | sort)
            == ["200", "400", "429", "500", "504"]
          )
      )
      and (($document.components.schemas["models.SearchRequestBody"] | type) == "object")
      and (($document.components.schemas["models.ItemType"] | type) == "object")
      and (($document.components.schemas["models.SearchResponse"] | type) == "object")
      and (($document.components.schemas["models.SearchRankerDescriptor"] | type) == "object")
      and (($document.components.schemas["models.SearchSources"] | type) == "object")
      and (($document.components.schemas["models.SearchSourceDiagnostic"] | type) == "object")
      and (($document.components.schemas["models.SearchExternalRetrieverDescriptor"] | type) == "object")
      and (($document.components.schemas["models.SearchPipeline"] | type) == "object")
      and (($document.components.schemas["models.SearchFacets"] | type) == "object")
      and (($document.components.schemas["models.SearchFacetValue"] | type) == "object")
      and (($document.components.securitySchemes.BearerAuth | type) == "object")
    )
    then $document
    else error("generated OpenAPI search operations or schemas are missing")
    end;

def search_request_schema:
  {
    "type": "object",
    "additionalProperties": false,
    "properties": {
      "q": {
        "type": "string",
        "maxLength": 256
      },
      "types": {
        "type": "array",
        "uniqueItems": true,
        "items": {
          "$ref": "#/components/schemas/models.ItemType"
        }
      },
      "page": {
        "type": "integer",
        "minimum": 1,
        "maximum": 1000,
        "default": 1
      },
      "per_page": {
        "type": "integer",
        "minimum": 1,
        "maximum": 100,
        "default": 10
      },
      "modalidade": {
        "type": "string",
        "enum": ["presencial", "digital", "hibrido"]
      },
      "bairro": {
        "type": "string",
        "maxLength": 100
      },
      "orgao": {
        "type": "string",
        "maxLength": 100
      },
      "turno": {
        "type": "string",
        "enum": ["matutino", "vespertino", "noturno"]
      },
      "regime_contratacao": {
        "type": "string",
        "enum": ["clt", "pj", "temporario"]
      },
      "modelo_trabalho": {
        "type": "string",
        "enum": ["presencial", "remoto", "hibrido"]
      },
      "pcd": {
        "type": "boolean"
      },
      "canal_atendimento": {
        "type": "string",
        "enum": ["presencial", "digital", "telefone"]
      },
      "tema": {
        "type": "string",
        "maxLength": 100
      },
      "segmento": {
        "type": "string",
        "maxLength": 100
      }
    }
  };

def string_error_response($description):
  {
    "description": $description,
    "content": {
      "application/json": {
        "schema": {
          "type": "object",
          "additionalProperties": {
            "type": "string"
          }
        }
      }
    }
  };

def request_id_header:
  {
    "description": "Canonical UUID used to correlate the response with application logs.",
    "schema": {
      "type": "string",
      "format": "uuid"
    }
  };

def metrics_path:
  {
    "get": {
      "description": "Returns metrics in Prometheus exposition format.",
      "summary": "Prometheus metrics",
      "tags": ["infra"],
      "security": [],
      "responses": {
        "200": {
          "description": "OK",
          "content": {
            "text/plain": {
              "schema": {
                "type": "string"
              }
            }
          }
        }
      }
    }
  };

def add_request_id_response_headers:
  .paths |= with_entries(
    .value |= with_entries(
      if ((.value.responses? | type) == "object")
      then .value.responses |= with_entries(
        .value.headers = ((.value.headers // {}) + {
          "X-Request-ID": {
            "$ref": "#/components/headers/X-Request-ID"
          }
        })
      )
      else .
      end
    )
  );

def search_facet_value_schema:
  {
    "type": "object",
    "additionalProperties": false,
    "required": ["value", "label", "count"],
    "properties": {
      "value": {
        "type": "string",
        "minLength": 1,
        "maxLength": 100,
        "description": "Canonical round-trip filter value; oversized and unsupported values are omitted."
      },
      "label": {
        "type": "string",
        "minLength": 1,
        "maxLength": 500,
        "description": "Bounded public display label."
      },
      "count": {
        "type": "integer",
        "minimum": 1
      }
    }
  };

def search_facets_schema:
  {
    "type": "object",
    "description": "Bounded facet counts sorted by count descending and canonical value ascending. Each canonical entity contributes at most once to a value. Counts apply all current filters, including the facet's own dimension.",
    "additionalProperties": false,
    "required": [
      "version",
      "scope",
      "types",
      "modalidades",
      "bairros",
      "organizations"
    ],
    "properties": {
      "version": {
        "type": "string",
        "enum": ["catalog-facets-v1"]
      },
      "scope": {
        "$ref": "#/components/schemas/models.SearchFacetScope"
      },
      "types": {
        "type": "array",
        "maxItems": 30,
        "items": {"$ref": "#/components/schemas/models.SearchFacetValue"}
      },
      "modalidades": {
        "type": "array",
        "maxItems": 30,
        "items": {"$ref": "#/components/schemas/models.SearchFacetValue"}
      },
      "bairros": {
        "type": "array",
        "maxItems": 30,
        "items": {"$ref": "#/components/schemas/models.SearchFacetValue"}
      },
      "organizations": {
        "type": "array",
        "maxItems": 30,
        "items": {"$ref": "#/components/schemas/models.SearchFacetValue"}
      }
    }
  };

def search_responses:
  {
    "200": {
      "description": "OK",
      "content": {
        "application/json": {
          "schema": {
            "$ref": "#/components/schemas/models.SearchResponse"
          }
        }
      }
    },
    "400": string_error_response("Bad Request"),
    "413": string_error_response("Request Entity Too Large"),
    "415": string_error_response("Unsupported Media Type"),
    "429": string_error_response("Too Many Requests"),
    "500": string_error_response("Internal Server Error"),
    "504": string_error_response("Gateway Timeout")
  };

def canonical_search_operation($summary; $security):
  .description = "Executa o mesmo pipeline da busca GET usando um corpo JSON estritamente validado, sem colocar o texto livre na URL."
  | .tags = ["busca"]
  | .summary = $summary
  | .security = $security
  | .requestBody = {
      "description": "Parâmetros da busca",
      "required": true,
      "content": {
        "application/json": {
          "schema": {
            "$ref": "#/components/schemas/models.SearchRequestBody"
          }
        }
      }
    }
  | .responses = search_responses;

def canonical_search_get_operation:
  .parameters |= map(
    if .name == "bairro" or .name == "orgao" or .name == "tema" or .name == "segmento"
    then .schema.maxLength = 100
    else .
    end
  )
  | .responses["429"] = string_error_response("Too Many Requests")
  | .responses["504"] = string_error_response("Gateway Timeout");

require_generated_search_contract
| .servers = [
    {
      "url": "https://services.staging.app.dados.rio/catalogo",
      "description": "Staging"
    },
    {
      "url": "https://services.pref.rio/catalogo",
      "description": "Produção"
    }
  ]
| .components.headers = ((.components.headers // {}) + {
    "X-Request-ID": request_id_header
  })
| .components.schemas["models.SearchRequestBody"] = search_request_schema
| .components.schemas["models.SearchFacetValue"] = search_facet_value_schema
| .components.schemas["models.SearchFacets"] = search_facets_schema
| .components.schemas["models.TargetAudienceData"] = target_audience_schema
| .components.schemas["models.CatalogItem"] |= (
    .additionalProperties = false
    | .["x-max-search-projection-bytes"] = 8192
    | .properties.external_id |= bounded_non_blank_text(255)
    | .properties.title |= bounded_non_blank_text(500)
    | .properties.description |= bounded_serialized_text(16000)
    | .properties.short_desc |= bounded_serialized_text(2000)
    | .properties.organization |= bounded_serialized_text(500)
    | .properties.modalidade |= bounded_serialized_text(100)
    | .properties.url |= bounded_catalog_http_url
    | .properties.image_url |= bounded_catalog_http_url
    | .properties.bairros |= bounded_catalog_string_array(1)
    | .properties.tags |= bounded_catalog_string_array(1)
    | .properties.target_audience = {"$ref": "#/components/schemas/models.TargetAudienceData"}
    | .properties.source_data = catalog_source_data_schema
  )
| .components.schemas["models.PublicCatalogItem"] |= (
    .additionalProperties = false
    | .properties.source_id |= bounded_non_blank_text(255)
    | .properties.title |= bounded_non_blank_text(500)
    | .properties.description |= bounded_serialized_text(16000)
    | .properties.short_desc |= bounded_serialized_text(2000)
    | .properties.organization |= bounded_serialized_text(500)
    | .properties.modalidade |= bounded_serialized_text(100)
    | .properties.url |= bounded_catalog_http_url
    | .properties.image_url |= bounded_catalog_http_url
    | .properties.bairros |= bounded_catalog_string_array(1)
    | .properties.tags |= bounded_catalog_string_array(1)
  )
| .components.schemas["models.RankedItem"] |= (
    .additionalProperties = false
    | .properties.title |= bounded_non_blank_text(500)
    | .properties.short_desc |= bounded_serialized_text(2000)
    | .properties.organization |= bounded_serialized_text(500)
    | .properties.modalidade |= bounded_serialized_text(100)
    | .properties.url |= bounded_catalog_http_url
    | .properties.image_url |= bounded_catalog_http_url
    | .properties.bairros |= bounded_catalog_string_array(1)
    | .properties.tags |= bounded_catalog_string_array(1)
  )
# Ranked scores and sync counters deliberately have no numeric bounds: the
# database still accepts unconstrained journey weights and event counts.
| .components.schemas["models.RecommendationResponse"].additionalProperties = false
| .components.schemas["models.RecommendationResponse"].properties.items.maxItems = 50
| .components.schemas["models.RecommendationErrorResponse"].additionalProperties = false
| .components.schemas["models.SyncStatus"].additionalProperties = false
| .components.schemas["models.SyncStatusResponse"].additionalProperties = false
| .components.schemas["models.SearchFacetScope"].description = "Population used for counts. catalog_matches counts canonical entities matching the current browse filters in one database snapshot; retrieval_candidates counts the bounded, canonically deduplicated candidate union before reranking and pagination; unavailable carries only empty facet arrays."
| .components.schemas["models.SearchRetrievalWeights"].required = [
    "exact",
    "full_text",
    "trigram",
    "semantic",
    "hyde",
    "facilita"
  ]
| .components.schemas["models.SearchRetrievalWeights"].additionalProperties = false
| .components.schemas["models.EmbeddingMetadata"].required = [
    "model",
    "version",
    "dimensions",
    "document_task_type",
    "query_task_type",
    "document_version"
  ]
| .components.schemas["models.EmbeddingMetadata"].additionalProperties = false
| .components.schemas["models.SearchRankerDescriptor"].required = [
    "schema_version",
    "base_version",
    "retrieval_version",
    "query_expansion_version",
    "deduplication_version",
    "candidate_pool_size",
    "semantic_overfetch_factor",
    "trigram_threshold",
    "maximum_semantic_distance",
    "reciprocal_rank_k",
    "weights",
    "semantic_enabled",
    "hyde_enabled",
    "reranker_enabled"
  ]
| .components.schemas["models.SearchRankerDescriptor"].additionalProperties = false
| .components.schemas["models.SearchRankerDescriptor"].properties.maximum_semantic_distance += {
    "minimum": 0,
    "exclusiveMinimum": true,
    "maximum": 2
  }
| .components.schemas["models.SearchSources"].required = ["facilita"]
| .components.schemas["models.SearchSources"].additionalProperties = false
| .components.schemas["models.SearchSourceDiagnostic"].required = [
    "status",
    "latency_ms",
    "candidates_received",
    "eligible_contributions"
  ]
| .components.schemas["models.SearchSourceDiagnostic"].additionalProperties = false
| .components.schemas["models.SearchSourceDiagnostic"].properties.latency_ms += {
    "minimum": 0
  }
| .components.schemas["models.SearchSourceDiagnostic"].properties.candidates_received += {
    "minimum": 0,
    "maximum": 50
  }
| .components.schemas["models.SearchSourceDiagnostic"].properties.eligible_contributions += {
    "minimum": 0,
    "maximum": 50
  }
| .components.schemas["models.SearchExternalRetrieverDescriptor"].required = [
    "schema_version",
    "catalog_revision",
    "retrieval_version",
    "query_expansion_version",
    "ranker_version"
  ]
| .components.schemas["models.SearchExternalRetrieverDescriptor"].additionalProperties = false
| .components.schemas["models.SearchExternalRetrieverDescriptor"].properties.schema_version |= bounded_non_blank_text(128)
| .components.schemas["models.SearchExternalRetrieverDescriptor"].properties.catalog_revision += {
    "minLength": 71,
    "maxLength": 71,
    "pattern": "^sha256:[0-9a-f]{64}$"
  }
| .components.schemas["models.SearchExternalRetrieverDescriptor"].properties.retrieval_version |= bounded_non_blank_text(128)
| .components.schemas["models.SearchExternalRetrieverDescriptor"].properties.query_expansion_version |= bounded_non_blank_text(128)
| .components.schemas["models.SearchExternalRetrieverDescriptor"].properties.ranker_version |= bounded_non_blank_text(128)
| .components.schemas["models.SearchItem"].required = [
    "id",
    "canonical_id",
    "type",
    "source",
    "source_id",
    "title",
    "relevance_score"
  ]
| .components.schemas["models.SearchItem"].properties.id.format = "uuid"
| .components.schemas["models.SearchItem"].properties.canonical_id.pattern = "^entity-v1:[0-9a-f]{64}$"
| .components.schemas["models.SearchItem"] |= (
    .additionalProperties = false
    | .properties.source_id |= bounded_non_blank_text(255)
    | .properties.slug |= bounded_serialized_text(500)
    | .properties.title |= bounded_non_blank_text(500)
    | .properties.short_desc |= bounded_serialized_text(2000)
    | .properties.organization |= bounded_serialized_text(500)
    | .properties.modalidade |= bounded_serialized_text(100)
    | .properties.url |= bounded_catalog_http_url
    | .properties.image_url |= bounded_catalog_http_url
    | .properties.bairros |= bounded_catalog_string_array(1)
    | .properties.tags |= bounded_catalog_string_array(1)
    | .properties.metadata = search_metadata_schema
  )
| .components.schemas["models.SearchResponse"].required = [
    "search_id",
    "ranker_version",
    "ranker_descriptor",
    "catalog_revision",
    "effective_pipeline",
    "degraded",
    "sources",
    "total",
    "page",
    "per_page",
    "facets",
    "items"
  ]
| .components.schemas["models.SearchResponse"].additionalProperties = false
| .components.schemas["models.SearchResponse"].properties.search_id.format = "uuid"
| .components.schemas["models.SearchResponse"].properties.total.description = "Total canonical entities in the declared browse or retrieval population before pagination."
| .components.schemas["v1.sfWebhookPayload"].required = ["sobject"]
| del(.components.schemas["v1.sfWebhookPayload"].properties.event.required)
| .components.schemas["v1.sfWebhookPayload"].properties.sobject.required = ["Id"]
| .components.schemas["v1.sfWebhookPayload"].properties.sobject.properties.Id += {
    "minLength": 1,
    "pattern": "\\S"
  }
| .paths["/metrics"] = metrics_path
| .paths["/api/v1/search"].post |= canonical_search_operation(
    "Busca autenticada por JSON";
    [{"BearerAuth": []}]
  )
| .paths["/api/public/search"].post |= canonical_search_operation(
    "Busca pública por JSON";
    []
  )
| .paths["/api/v1/search"].get |= canonical_search_get_operation
| .paths["/api/public/search"].get |= canonical_search_get_operation
| .paths["/api/v1/search"].get.security = bearer_security
| .paths["/api/v1/search"].get.responses["401"] = string_error_response("Unauthorized")
| .paths["/api/v1/search"].post.responses["401"] = string_error_response("Unauthorized")
| .paths["/api/webhooks/salesforce"].post.parameters |= map(
    if .name == "X-Salesforce-Signature"
    then .schema += {
      "minLength": 64,
      "maxLength": 64,
      "pattern": "^[0-9A-Fa-f]{64}$"
    }
    else .
    end
  )
| .paths["/api/webhooks/salesforce"].post.requestBody["x-max-body-bytes"] = 65536
| add_request_id_response_headers
