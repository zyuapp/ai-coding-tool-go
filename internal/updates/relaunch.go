package updates

import (
	"log"
	"os"
	"path/filepath"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

const relaunchRecord = "update-relaunch.json"

// RecordUpdateRelaunch writes down the threads to carry on once version is the build running.
func RecordUpdateRelaunch(userData, version string, taskIDs []string) {
	record := filepath.Join(userData, relaunchRecord)
	var err error
	if len(taskIDs) > 0 {
		var data []byte
		data, err = jsonx.Marshal(struct {
			Version string   `json:"version"`
			TaskIDs []string `json:"taskIds"`
		}{version, taskIDs})
		if err == nil {
			err = os.WriteFile(record, data, 0o666)
		}
	} else if err = os.Remove(record); os.IsNotExist(err) {
		err = nil
	}
	if err != nil {
		log.Printf("Could not record the threads to resume after updating: %v", err)
	}
}

// TakeUpdateRelaunch is the threads the last update cut off, read once. A record left by an install
// that never took is for another version, and is dropped.
func TakeUpdateRelaunch(userData, version string) []string {
	record := filepath.Join(userData, relaunchRecord)
	raw, err := os.ReadFile(record)
	if err != nil {
		return []string{}
	}
	_ = os.Remove(record)
	var parsed any
	if jsonx.Unmarshal(raw, &parsed) != nil {
		return []string{}
	}
	fields, ok := parsed.(map[string]any)
	if !ok {
		return []string{}
	}
	recorded, _ := fields["version"].(string)
	ids, isList := fields["taskIds"].([]any)
	if _, isString := fields["version"].(string); !isString || recorded != version || !isList {
		return []string{}
	}
	taskIDs := []string{}
	for _, id := range ids {
		if text, ok := id.(string); ok {
			taskIDs = append(taskIDs, text)
		}
	}
	return taskIDs
}
