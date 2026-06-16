package api

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"kt-ai-studio/internal/db"
	"kt-ai-studio/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	textToVideoOutputRoot = "output/text_to_video"
	// Default RunningHub-hosted LTX2.3 text-to-video workflow (workflowId mapped in
	// runninghub_workflow_map). Overridable per-section via section_workflow_map.
	textToVideoWorkflowPath = "workflows/rh_ltx2_3_t2v.json"
)

func textToVideoProjectDir(code string) string {
	return filepath.Join(textToVideoOutputRoot, code)
}

func textToVideoVideosDir(code string) string {
	return filepath.Join(textToVideoProjectDir(code), "videos")
}

type textToVideoParsedLine struct {
	SortOrder int
	Prompt    string
}

// parseTextToVideoText splits the project text into prompts. Prompts are separated
// by a blank line (so a single prompt may span multiple lines), falling back to
// one-prompt-per-line when no blank lines are present.
func parseTextToVideoText(text string) ([]textToVideoParsedLine, error) {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	var blocks []string
	if strings.Contains(normalized, "\n\n") {
		for _, b := range strings.Split(normalized, "\n\n") {
			if strings.TrimSpace(b) != "" {
				blocks = append(blocks, strings.TrimSpace(b))
			}
		}
	} else {
		for _, b := range strings.Split(normalized, "\n") {
			if strings.TrimSpace(b) != "" {
				blocks = append(blocks, strings.TrimSpace(b))
			}
		}
	}
	lines := make([]textToVideoParsedLine, 0, len(blocks))
	for _, b := range blocks {
		lines = append(lines, textToVideoParsedLine{SortOrder: len(lines) + 1, Prompt: b})
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("请先填写至少一段提示词")
	}
	return lines, nil
}

func loadTextToVideoProjectOr404(c *gin.Context) (*models.TextToVideoProject, error) {
	id := strings.TrimSpace(c.Param("id"))
	var project models.TextToVideoProject
	if err := db.DB.First(&project, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "文生视频项目不存在"})
		return nil, err
	}
	return &project, nil
}

func loadTextToVideoLineOr404(c *gin.Context) (*models.TextToVideoLine, error) {
	id := strings.TrimSpace(c.Param("lineId"))
	var line models.TextToVideoLine
	if err := db.DB.First(&line, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "文生视频行不存在"})
		return nil, err
	}
	return &line, nil
}

func replaceTextToVideoLines(project models.TextToVideoProject, text string) ([]models.TextToVideoLine, error) {
	parsed, err := parseTextToVideoText(text)
	if err != nil {
		return nil, err
	}
	oldVideos := make([]string, 0)
	created := make([]models.TextToVideoLine, 0, len(parsed))
	now := time.Now()
	err = db.DB.Transaction(func(tx *gorm.DB) error {
		var oldLines []models.TextToVideoLine
		if err := tx.Where("project_id = ?", project.ID).Find(&oldLines).Error; err != nil {
			return err
		}
		for _, line := range oldLines {
			if strings.TrimSpace(line.GeneratedVideo) != "" {
				oldVideos = append(oldVideos, line.GeneratedVideo)
			}
		}
		if err := tx.Where("project_id = ?", project.ID).Delete(&models.TextToVideoLine{}).Error; err != nil {
			return err
		}
		for _, item := range parsed {
			line := models.TextToVideoLine{
				ProjectID: project.ID,
				SortOrder: item.SortOrder,
				Prompt:    item.Prompt,
				Seed:      -1,
				Status:    audioCloneLineStatusDraft,
				CreatedAt: now,
				UpdatedAt: now,
			}
			if err := tx.Create(&line).Error; err != nil {
				return err
			}
			created = append(created, line)
		}
		return nil
	})
	if err == nil {
		for _, p := range oldVideos {
			if abs, e := assetWebPathToAbs(p); e == nil {
				_ = os.Remove(abs)
			}
		}
	}
	return created, err
}

func ListTextToVideoProjects(c *gin.Context) {
	var projects []models.TextToVideoProject
	if err := db.DB.Order("created_at desc").Find(&projects).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取文生视频项目失败"})
		return
	}
	c.JSON(http.StatusOK, projects)
}

func GetTextToVideoProject(c *gin.Context) {
	project, err := loadTextToVideoProjectOr404(c)
	if err != nil {
		return
	}
	c.JSON(http.StatusOK, project)
}

func CreateTextToVideoProject(c *gin.Context) {
	var req struct {
		Name        string `json:"name"`
		Code        string `json:"code"`
		Description string `json:"description"`
		Text        string `json:"text"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	name := strings.TrimSpace(req.Name)
	code := normalizeAudioCloneCode(req.Code)
	if name == "" || code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写项目名称和项目文件名"})
		return
	}
	if !validateAudioCloneProjectCode(code) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "项目文件名只允许英文、数字、下划线或连字符"})
		return
	}
	var count int64
	db.DB.Model(&models.TextToVideoProject{}).Where("code = ?", code).Count(&count)
	if count > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "项目文件名已被占用"})
		return
	}
	project := models.TextToVideoProject{
		Name:        name,
		Code:        code,
		Description: strings.TrimSpace(req.Description),
		Text:        strings.TrimSpace(req.Text),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := os.MkdirAll(textToVideoProjectDir(code), 0755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建项目目录失败"})
		return
	}
	if err := db.DB.Create(&project).Error; err != nil {
		_ = os.RemoveAll(textToVideoProjectDir(code))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建文生视频项目失败"})
		return
	}
	c.JSON(http.StatusCreated, project)
}

func UpdateTextToVideoProject(c *gin.Context) {
	project, err := loadTextToVideoProjectOr404(c)
	if err != nil {
		return
	}
	var req struct {
		Name        string `json:"name"`
		Code        string `json:"code"`
		Description string `json:"description"`
		Text        string `json:"text"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	name := strings.TrimSpace(req.Name)
	code := normalizeAudioCloneCode(req.Code)
	if name == "" || code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写项目名称和项目文件名"})
		return
	}
	if !validateAudioCloneProjectCode(code) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "项目文件名只允许英文、数字、下划线或连字符"})
		return
	}
	if code != project.Code {
		var count int64
		db.DB.Model(&models.TextToVideoProject{}).Where("code = ? AND id <> ?", code, project.ID).Count(&count)
		if count > 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "项目文件名已被占用"})
			return
		}
		if err := os.Rename(textToVideoProjectDir(project.Code), textToVideoProjectDir(code)); err != nil && !os.IsNotExist(err) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "重命名项目目录失败"})
			return
		}
		oldPrefix := "/" + filepath.ToSlash(textToVideoProjectDir(project.Code))
		newPrefix := "/" + filepath.ToSlash(textToVideoProjectDir(code))
		var lines []models.TextToVideoLine
		_ = db.DB.Where("project_id = ?", project.ID).Find(&lines).Error
		for _, line := range lines {
			if strings.HasPrefix(line.GeneratedVideo, oldPrefix) {
				_ = db.DB.Model(&models.TextToVideoLine{}).Where("id = ?", line.ID).Update("generated_video", newPrefix+strings.TrimPrefix(line.GeneratedVideo, oldPrefix)).Error
			}
		}
	}
	if err := db.DB.Model(&models.TextToVideoProject{}).Where("id = ?", project.ID).Updates(map[string]interface{}{
		"name":        name,
		"code":        code,
		"description": strings.TrimSpace(req.Description),
		"text":        strings.TrimSpace(req.Text),
		"updated_at":  time.Now(),
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新文生视频项目失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "项目已更新"})
}

func DeleteTextToVideoProject(c *gin.Context) {
	project, err := loadTextToVideoProjectOr404(c)
	if err != nil {
		return
	}
	if err := db.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("project_id = ?", project.ID).Delete(&models.TextToVideoLine{}).Error; err != nil {
			return err
		}
		return tx.Delete(&models.TextToVideoProject{}, project.ID).Error
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除文生视频项目失败"})
		return
	}
	_ = os.RemoveAll(textToVideoProjectDir(project.Code))
	c.JSON(http.StatusOK, gin.H{"message": "项目已删除"})
}

func ListTextToVideoLines(c *gin.Context) {
	project, err := loadTextToVideoProjectOr404(c)
	if err != nil {
		return
	}
	var lines []models.TextToVideoLine
	if err := db.DB.Where("project_id = ?", project.ID).Order("sort_order asc, id asc").Find(&lines).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取文生视频行失败"})
		return
	}
	c.JSON(http.StatusOK, lines)
}

func SaveTextToVideoLines(c *gin.Context) {
	project, err := loadTextToVideoProjectOr404(c)
	if err != nil {
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	lines, err := replaceTextToVideoLines(*project, req.Text)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	_ = db.DB.Model(&models.TextToVideoProject{}).Where("id = ?", project.ID).Updates(map[string]interface{}{
		"text":       strings.TrimSpace(req.Text),
		"updated_at": time.Now(),
	}).Error
	c.JSON(http.StatusOK, gin.H{"lines": lines})
}

func GenerateTextToVideoProjectLines(c *gin.Context) {
	project, err := loadTextToVideoProjectOr404(c)
	if err != nil {
		return
	}
	var req struct {
		Text       string `json:"text"`
		RandomSeed bool   `json:"random_seed"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	lines, err := replaceTextToVideoLines(*project, req.Text)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	_ = db.DB.Model(&models.TextToVideoProject{}).Where("id = ?", project.ID).Updates(map[string]interface{}{
		"text":       strings.TrimSpace(req.Text),
		"updated_at": time.Now(),
	}).Error

	for _, line := range lines {
		seed := getConfiguredGlobalSeed()
		if req.RandomSeed {
			seed = randomAudioProductionSeed()
		}
		taskID, err := startTextToVideoLineTask(&line, project, seed)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "提交生成任务失败"})
			return
		}
		_ = db.DB.Model(&models.TextToVideoLine{}).Where("id = ?", line.ID).Updates(map[string]interface{}{
			"status":          audioCloneLineStatusGenerating,
			"current_task_id": taskID,
			"seed":            seed,
			"last_error":      "",
			"updated_at":      time.Now(),
		}).Error
	}
	c.JSON(http.StatusOK, gin.H{"message": "文生视频任务已提交", "lines": len(lines)})
}

func GenerateTextToVideoLine(c *gin.Context) {
	line, err := loadTextToVideoLineOr404(c)
	if err != nil {
		return
	}
	var project models.TextToVideoProject
	if err := db.DB.First(&project, line.ProjectID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "所属项目不存在"})
		return
	}
	var req struct {
		RandomSeed bool `json:"random_seed"`
	}
	_ = c.ShouldBindJSON(&req)
	seed := getConfiguredGlobalSeed()
	if req.RandomSeed {
		seed = randomAudioProductionSeed()
	}
	taskID, err := startTextToVideoLineTask(line, &project, seed)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "提交生成任务失败"})
		return
	}
	_ = db.DB.Model(&models.TextToVideoLine{}).Where("id = ?", line.ID).Updates(map[string]interface{}{
		"status":          audioCloneLineStatusGenerating,
		"current_task_id": taskID,
		"seed":            seed,
		"last_error":      "",
		"updated_at":      time.Now(),
	}).Error
	c.JSON(http.StatusOK, gin.H{"message": "文生视频任务已提交", "task_id": taskID})
}
