package registry

import "testing"

func TestGetCodeBuddyModelsIncludesVerifiedBuiltIns(t *testing.T) {
	models := GetCodeBuddyModels()
	byID := make(map[string]*ModelInfo, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		if _, exists := byID[model.ID]; exists {
			t.Fatalf("duplicate CodeBuddy model ID %q", model.ID)
		}
		byID[model.ID] = model
	}

	for _, id := range []string{
		"auto",
		"hy4",
		"hy3",
		"deepseek-v4.1-flash",
		"deepseek-v4-pro",
		"glm-5.3",
		"glm-5.3-flash",
		"glm-5.2",
		"kimi-k3",
		"kimi-k3.1-pro",
		"kimi-k2.8-preview",
		"minimax-m3.1-flash",
		"space-bunny",
		"step-5-preview",
		"mimo-v2.6-pro",
	} {
		if byID[id] == nil {
			t.Fatalf("expected CodeBuddy model %q to be registered", id)
		}
	}

	for _, id := range []string{"hy3", "hy4"} {
		if byID[id].Thinking == nil {
			t.Fatalf("expected %s to advertise thinking support", id)
		}
	}

	for _, removedID := range []string{"hy4-preview", "hy3-preview", "minimax-m2.5", "kimi-k2.6", "kimi-k2-thinking"} {
		if byID[removedID] != nil {
			t.Fatalf("expected %s to be removed", removedID)
		}
	}
}
