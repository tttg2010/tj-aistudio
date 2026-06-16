package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"kt-ai-studio/internal/db"
	"kt-ai-studio/internal/models"
	"kt-ai-studio/internal/workflow"

	"github.com/gin-gonic/gin"
)

// resolveSectionWorkflowFile returns the workflow path a section+media should use:
// a user override from section_workflow_map ("<section>.<media>" → file basename)
// if it exists on disk, otherwise fallbackPath. Lets each generate action's
// workflow be changed from the UI and take effect on the next generation.
func resolveSectionWorkflowFile(section, media, fallbackPath string) string {
	raw := strings.TrimSpace(getSettingValue(KeySectionWorkflowMap))
	if raw != "" && raw != "{}" {
		m := map[string]string{}
		if json.Unmarshal([]byte(raw), &m) == nil {
			if f := strings.TrimSpace(m[section+"."+media]); f != "" {
				p := filepath.Join("workflows", filepath.Base(f))
				if info, err := os.Stat(p); err == nil && !info.IsDir() {
					return p
				}
			}
		}
	}
	return fallbackPath
}

// storeVisitVideoWorkflowFile / generalGuideVideoWorkflowFile resolve the video
// workflow for their section: a user override wins, else the provider-conditional
// default (LTX local / WAN RunningHub).
func storeVisitVideoWorkflowFile() string {
	return resolveSectionWorkflowFile("store_visit", "video", videoWorkflowFileForProvider())
}

func generalGuideVideoWorkflowFile() string {
	return resolveSectionWorkflowFile("general_guide", "video", videoWorkflowFileForProvider())
}

// SetSectionWorkflow persists a per-section workflow override. Short-drama is
// special: it already resolves via the default_image_model / default_video_model
// settings, so we write the workflow's display name there instead of the map.
func SetSectionWorkflow(c *gin.Context) {
	var body struct {
		Section  string `json:"section"`
		Media    string `json:"media"`
		Workflow string `json:"workflow"` // file basename
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	section := strings.TrimSpace(body.Section)
	media := strings.TrimSpace(body.Media)
	rawWorkflow := strings.TrimSpace(body.Workflow)
	if section == "" || media == "" || rawWorkflow == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "section/media/workflow 不能为空"})
		return
	}

	// "__default__" clears the override so the section falls back to its built-in
	// (provider-conditional) default. Not applicable to short-drama, which always
	// resolves via the default-model setting.
	if rawWorkflow == "__default__" {
		if section == "short_drama" {
			c.JSON(http.StatusOK, gin.H{"message": "短剧工作流由默认模型设置控制"})
			return
		}
		m := map[string]string{}
		if raw := strings.TrimSpace(getSettingValue(KeySectionWorkflowMap)); raw != "" {
			_ = json.Unmarshal([]byte(raw), &m)
		}
		delete(m, section+"."+media)
		encoded, _ := json.Marshal(m)
		if err := upsertSetting(KeySectionWorkflowMap, string(encoded)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "已恢复默认", "workflow": "__default__"})
		return
	}

	file := filepath.Base(rawWorkflow)
	path := filepath.Join("workflows", file)
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "工作流文件不存在: " + file})
		return
	}

	if section == "short_drama" {
		// Map file → workflow display name and store in the default model setting.
		meta, err := workflow.ParseWorkflow(path)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无法解析工作流: " + err.Error()})
			return
		}
		key := KeyDefaultImageModel
		if media == "video" {
			key = KeyDefaultVideoModel
		}
		if err := upsertSetting(key, meta.WorkflowName); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "已更新", "workflow": file})
		return
	}

	// Update section_workflow_map.
	m := map[string]string{}
	if raw := strings.TrimSpace(getSettingValue(KeySectionWorkflowMap)); raw != "" {
		_ = json.Unmarshal([]byte(raw), &m)
	}
	m[section+"."+media] = file
	encoded, _ := json.Marshal(m)
	if err := upsertSetting(KeySectionWorkflowMap, string(encoded)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已更新", "workflow": file})
}

// upsertSetting creates or updates a single setting row.
func upsertSetting(key, value string) error {
	var count int64
	db.DB.Model(&models.SystemSettings{}).Where("key = ?", key).Count(&count)
	if count == 0 {
		return db.DB.Create(&models.SystemSettings{Key: key, Value: value, Description: getDescription(key)}).Error
	}
	return db.DB.Model(&models.SystemSettings{}).Where("key = ?", key).Update("value", value).Error
}

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
		makeActiveWorkflowEntry("store_visit", "探店·图片", "image", resolveSectionWorkflowFile("store_visit", "image", storeVisitImageWorkflowPath), imgP),
		makeActiveWorkflowEntry("store_visit", "探店·视频", "video", storeVisitVideoWorkflowFile(), vidP),
		makeActiveWorkflowEntry("general_guide", "综合讲解·图片", "image", resolveSectionWorkflowFile("general_guide", "image", generalGuideImageWorkflowPath), imgP),
		makeActiveWorkflowEntry("general_guide", "综合讲解·视频", "video", generalGuideVideoWorkflowFile(), vidP),
		// Face-closeup first so the primary multi-angle workflow wins by_section["multi_visual"]["image"].
		makeActiveWorkflowEntry("multi_visual", "多视觉·脸部特写", "image", multiVisualFaceCloseupWorkflowPath, imgP),
		makeActiveWorkflowEntry("multi_visual", "多视觉·多角度图", "image", resolveSectionWorkflowFile("multi_visual", "image", multiVisualWorkflowPath), imgP),
		makeActiveWorkflowEntry("audio_production_custom_voice", "配音·按人设生成", "audio", resolveSectionWorkflowFile("audio_production_custom_voice", "audio", audioProductionCustomVoiceWorkflowPath), audP),
		makeActiveWorkflowEntry("audio_production_voice_prompt", "配音·按提示生成", "audio", resolveSectionWorkflowFile("audio_production_voice_prompt", "audio", audioProductionVoicePromptWorkflowPath), audP),
		makeActiveWorkflowEntry("qwen_tts", "Qwen3 TTS 语音克隆", "audio", resolveSectionWorkflowFile("qwen_tts", "audio", qwenTTSWorkflowPath), audP),
		makeActiveWorkflowEntry("audio_clone", "LongCat 语音克隆", "audio", resolveSectionWorkflowFile("audio_clone", "audio", audioCloneWorkflowPath), audP),
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
