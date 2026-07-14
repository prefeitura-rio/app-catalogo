package evaluation

import "github.com/prefeitura-rio/app-catalogo/internal/models"

func testDocument(source models.ItemSource, sourceID string) DocumentKey {
	return DocumentKey{Source: source, SourceID: sourceID}
}

func testJudgment(entityID string, grade int, documents ...DocumentKey) EntityJudgment {
	return EntityJudgment{EntityID: entityID, Grade: grade, Documents: documents}
}
