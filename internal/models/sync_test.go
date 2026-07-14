package models

import (
	"encoding/json"
	"testing"
)

func TestNewSyncStatusResponseSerializesMissingStatusesAsEmptyArray(t *testing.T) {
	t.Parallel()

	encodedResponse, encodeError := json.Marshal(NewSyncStatusResponse(nil))
	if encodeError != nil {
		t.Fatalf("marshal sync status response: %v", encodeError)
	}
	if string(encodedResponse) != `{"syncs":[]}` {
		t.Fatalf("empty sync status response = %s, want non-null array", encodedResponse)
	}
}
