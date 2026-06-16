package api

import (
	"archive/zip"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"kt-ai-studio/internal/db"
	"kt-ai-studio/internal/models"

	"github.com/gin-gonic/gin"
)

func listTextToVideoLinesForExport(projectID uint) ([]models.TextToVideoLine, error) {
	var lines []models.TextToVideoLine
	if err := db.DB.Where("project_id = ?", projectID).Order("sort_order asc, id asc").Find(&lines).Error; err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("当前项目还没有可导出的提示词")
	}
	missing := make([]string, 0)
	for idx, line := range lines {
		if qwenTTSExportAssetReady(line.GeneratedVideo) {
			continue
		}
		missing = append(missing, fmt.Sprintf("%d", idx+1))
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("还有 %d 段未生成视频，暂不能导出：第 %s 段", len(missing), strings.Join(missing, "、"))
	}
	return lines, nil
}

func buildTextToVideoExportText(lines []models.TextToVideoLine) string {
	rows := make([]string, 0, len(lines))
	for idx, line := range lines {
		n := line.SortOrder
		if n <= 0 {
			n = idx + 1
		}
		rows = append(rows, fmt.Sprintf("%d:%s", n, strings.TrimSpace(line.Prompt)))
	}
	return strings.Join(rows, "\n")
}

func buildTextToVideoExportArchive(lines []models.TextToVideoLine, zipPath string) error {
	zipFile, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer zipFile.Close()
	zipWriter := zip.NewWriter(zipFile)
	defer zipWriter.Close()

	for idx, line := range lines {
		n := line.SortOrder
		if n <= 0 {
			n = idx + 1
		}
		sourcePath, err := assetWebPathToAbs(line.GeneratedVideo)
		if err != nil {
			return err
		}
		ext := filepath.Ext(sourcePath)
		if ext == "" {
			ext = ".mp4"
		}
		if err := addFileToZip(zipWriter, fmt.Sprintf("%d%s", n, ext), sourcePath); err != nil {
			return err
		}
	}
	return addTextToZip(zipWriter, "all.txt", buildTextToVideoExportText(lines))
}

// ExportTextToVideoProjectArchive zips all generated clips + an all.txt manifest.
func ExportTextToVideoProjectArchive(c *gin.Context) {
	project, err := loadTextToVideoProjectOr404(c)
	if err != nil {
		return
	}
	lines, err := listTextToVideoLinesForExport(project.ID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	workspaceDir, err := createTemporaryVideoExportWorkspace(fmt.Sprintf("text_to_video_%d_export_", project.ID))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("导出空间创建失败: %v", err)})
		return
	}
	filenameBase := sanitizeQwenTTSExportFilename(project.Code)
	if filenameBase == "未命名" {
		filenameBase = fmt.Sprintf("text_to_video_%d", project.ID)
	}
	zipPath := filepath.Join(workspaceDir, fmt.Sprintf("%s_export.zip", filenameBase))
	if err := buildTextToVideoExportArchive(lines, zipPath); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("构建导出压缩包失败: %v", err)})
		return
	}
	c.FileAttachment(zipPath, filepath.Base(zipPath))
}
