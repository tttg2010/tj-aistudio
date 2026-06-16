package api

import (
	"net/http"
	"path/filepath"
	"strings"

	"kt-ai-studio/internal/db"
	"kt-ai-studio/internal/models"
	"kt-ai-studio/internal/workflow"

	"github.com/gin-gonic/gin"
)

// activeWorkflowEntry describes the workflow a given generation action will
// actually use right now, accounting for the configured provider. Surfaced to
// the UI (next to generate buttons) as a debugging aid.
type activeWorkflowEntry struct {
	Section   string `json:"section"`
	Label     string `json:"label"`
	MediaType string `json:"media_type"` // image | video | audio
	Workflow  string `json:"workflow"`   // workflow file basename, or "(未设置)"
	Provider  string `json:"provider"`   // local | runninghub | jimeng
	// RHMapped is meaningful only when Provider == runninghub: whether this
	// workflow file has a RunningHub workflowId mapping in settings.
	RHMapped bool `json:"rh_mapped"`
}

// resolveWorkflowFilenameByName resolves a workflow display name (as stored in
// default_image_model / default_video_model) to its file basename.
func resolveWorkflowFilenameByName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	files, _ := filepath.Glob(filepath.Join("workflows", "*.json"))
	for _, f := range files {
		if meta, err := workflow.ParseWorkflow(f); err == nil && meta.WorkflowName == name {
			return filepath.Base(f)
		}
	}
	return ""
}

func makeActiveWorkflowEntry(section, label, media, workflowFile, provider string) activeWorkflowEntry {
	e := activeWorkflowEntry{Section: section, Label: label, MediaType: media, Provider: provider}
	if strings.TrimSpace(workflowFile) == "" {
		e.Workflow = "(未设置)"
		return e
	}
	e.Workflow = filepath.Base(workflowFile)
	if provider == ImageGenerationProviderRunningHub { // "runninghub" for all modalities
		e.RHMapped = lookupRunningHubWorkflowID(e.Workflow) != ""
	}
	return e
}

// GetActiveWorkflows returns, per section + media type, the workflow each
// generation action will currently use (resolving provider-conditional choices).
func GetActiveWorkflows(c *gin.Context) {
	imgP := getConfiguredImageGenerationProvider()
	vidP := getConfiguredVideoGenerationProvider()
	audP := getConfiguredAudioGenerationProvider()

	var imgModel, vidModel models.SystemSettings
	db.DB.Where("key = ?", KeyDefaultImageModel).First(&imgModel)
	db.DB.Where("key = ?", KeyDefaultVideoModel).First(&vidModel)
	shortImg := resolveWorkflowFilenameByName(imgModel.Value)
	shortVid := resolveWorkflowFilenameByName(vidModel.Value)

	entries := []activeWorkflowEntry{
		makeActiveWorkflowEntry("short_drama", "短剧·角色/场景图片", "image", shortImg, imgP),
		makeActiveWorkflowEntry("short_drama", "短剧·视频", "video", shortVid, vidP),
		makeActiveWorkflowEntry("store_visit", "探店·图片", "image", storeVisitImageWorkflowPath, imgP),
		makeActiveWorkflowEntry("store_visit", "探店·视频", "video", videoWorkflowFileForProvider(), vidP),
		makeActiveWorkflowEntry("general_guide", "综合讲解·图片", "image", generalGuideImageWorkflowPath, imgP),
		makeActiveWorkflowEntry("general_guide", "综合讲解·视频", "video", videoWorkflowFileForProvider(), vidP),
		// Face-closeup first so the primary multi-angle workflow wins by_section["multi_visual"]["image"].
		makeActiveWorkflowEntry("multi_visual", "多视觉·脸部特写", "image", multiVisualFaceCloseupWorkflowPath, imgP),
		makeActiveWorkflowEntry("multi_visual", "多视觉·多角度图", "image", multiVisualWorkflowPath, imgP),
		makeActiveWorkflowEntry("audio_production_custom_voice", "配音·按人设生成", "audio", audioProductionCustomVoiceWorkflowPath, audP),
		makeActiveWorkflowEntry("audio_production_voice_prompt", "配音·按提示生成", "audio", audioProductionVoicePromptWorkflowPath, audP),
		makeActiveWorkflowEntry("qwen_tts", "Qwen3 TTS 语音克隆", "audio", qwenTTSWorkflowPath, audP),
		makeActiveWorkflowEntry("audio_clone", "LongCat 语音克隆", "audio", audioCloneWorkflowPath, audP),
	}

	bySection := map[string]map[string]activeWorkflowEntry{}
	for _, e := range entries {
		if bySection[e.Section] == nil {
			bySection[e.Section] = map[string]activeWorkflowEntry{}
		}
		bySection[e.Section][e.MediaType] = e
	}

	c.JSON(http.StatusOK, gin.H{"entries": entries, "by_section": bySection})
}
