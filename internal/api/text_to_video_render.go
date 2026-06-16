package api

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"kt-ai-studio/internal/db"
	"kt-ai-studio/internal/models"
	"kt-ai-studio/internal/task"
)

type textToVideoLineTaskPayload struct {
	ProjectID uint  `json:"project_id"`
	LineID    uint  `json:"line_id"`
	Seed      int64 `json:"seed"`
}

func startTextToVideoLineTask(line *models.TextToVideoLine, project *models.TextToVideoProject, seed int64) (string, error) {
	payload := textToVideoLineTaskPayload{
		ProjectID: project.ID,
		LineID:    line.ID,
		Seed:      seed,
	}
	taskRecord, err := task.GlobalTaskManager.AddTask("render_text_to_video_line", payload)
	if err != nil {
		return "", err
	}
	return taskRecord.ID, nil
}

func shouldApplyTextToVideoLineTaskResult(lineID uint, taskID string) bool {
	var line models.TextToVideoLine
	if err := db.DB.Select("id", "current_task_id", "status").First(&line, lineID).Error; err != nil {
		return false
	}
	return strings.TrimSpace(line.CurrentTaskID) == taskID && line.Status == audioCloneLineStatusGenerating
}

func markTextToVideoLineFailed(lineID uint, taskID string, err error) {
	if !shouldApplyTextToVideoLineTaskResult(lineID, taskID) {
		return
	}
	_ = db.DB.Model(&models.TextToVideoLine{}).Where("id = ?", lineID).Updates(map[string]interface{}{
		"status":          audioCloneLineStatusFailed,
		"current_task_id": "",
		"last_error":      err.Error(),
		"updated_at":      time.Now(),
	}).Error
}

// injectTextToVideoNodes injects the prompt/seed/negative into the LTX2.3 t2v
// workflow's nodes: the prompt lives on a "CR Text" node, the seed on RandomNoise,
// and the negative on the (string-valued) CLIPTextEncode node. Additive.
func injectTextToVideoNodes(graph map[string]interface{}, prompt, negative string, seed int64) {
	for _, raw := range graph {
		n, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		ct, _ := n["class_type"].(string)
		inputs, ok := n["inputs"].(map[string]interface{})
		if !ok {
			continue
		}
		switch ct {
		case "CR Text":
			if _, has := inputs["text"]; has && strings.TrimSpace(prompt) != "" {
				inputs["text"] = prompt
			}
		case "RandomNoise":
			if _, has := inputs["noise_seed"]; has {
				inputs["noise_seed"] = seed
			}
		case "CLIPTextEncode":
			// Only the negative prompt is a raw string here (the positive is a node
			// link); override it only when the user supplied a negative.
			if t, ok := inputs["text"].(string); ok && t != "" && strings.TrimSpace(negative) != "" {
				inputs["text"] = strings.TrimSpace(negative)
			}
		}
	}
}

// HandleRenderTextToVideoLineTask generates one clip from a prompt via the
// RunningHub-hosted LTX2.3 text-to-video workflow. This section is RunningHub-only.
func HandleRenderTextToVideoLineTask(t *models.Task) (interface{}, error) {
	var payload textToVideoLineTaskPayload
	if err := json.Unmarshal([]byte(t.Payload), &payload); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	var project models.TextToVideoProject
	if err := db.DB.First(&project, payload.ProjectID).Error; err != nil {
		return nil, fmt.Errorf("project not found: %w", err)
	}
	var line models.TextToVideoLine
	if err := db.DB.First(&line, payload.LineID).Error; err != nil {
		return nil, fmt.Errorf("line not found: %w", err)
	}
	if strings.TrimSpace(line.Prompt) == "" {
		err := fmt.Errorf("提示词为空")
		markTextToVideoLineFailed(line.ID, t.ID, err)
		return nil, err
	}

	wfPath := resolveSectionWorkflowFile("text_to_video", "video", textToVideoWorkflowPath)
	template, err := loadStoreVisitWorkflowTemplate(wfPath)
	if err != nil {
		markTextToVideoLineFailed(line.ID, t.ID, err)
		return nil, err
	}
	injected, err := cloneStoreVisitWorkflow(template)
	if err != nil {
		markTextToVideoLineFailed(line.ID, t.ID, err)
		return nil, err
	}

	seed := payload.Seed
	if seed < 0 {
		seed = getConfiguredGlobalSeed()
	}
	injectTextToVideoNodes(injected, line.Prompt, line.NegativePrompt, seed)

	workflowLabel := workflowDisplayNameFromPath(wfPath) + "（RunningHub）"
	logComfyWorkflowPayload("Text To Video Payload", workflowLabel, injected)
	task.GlobalTaskManager.UpdateTaskProgress(t.ID, 40, "")

	saveDir := textToVideoVideosDir(project.Code)
	fileBase := fmt.Sprintf("line_%02d_%d", line.SortOrder, line.ID)
	webPath, err := runRunningHubVideoTask(filepath.Base(wfPath), template, injected, saveDir, fileBase)
	if err != nil {
		markTextToVideoLineFailed(line.ID, t.ID, err)
		return nil, err
	}
	if strings.TrimSpace(webPath) == "" {
		err = fmt.Errorf("未获取到文生视频输出")
		markTextToVideoLineFailed(line.ID, t.ID, err)
		return nil, err
	}
	if !shouldApplyTextToVideoLineTaskResult(line.ID, t.ID) {
		return map[string]interface{}{"skipped": true}, nil
	}
	if err := db.DB.Model(&models.TextToVideoLine{}).Where("id = ?", line.ID).Updates(map[string]interface{}{
		"generated_video":    webPath,
		"status":             audioCloneLineStatusGenerated,
		"current_task_id":    "",
		"last_error":         "",
		"generated_workflow": workflowLabel,
		"updated_at":         time.Now(),
	}).Error; err != nil {
		return nil, err
	}
	return map[string]interface{}{"generated_video": webPath}, nil
}
