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

func listAudioProductionLinesForExport(projectID uint) ([]models.AudioProductionLine, error) {
	var lines []models.AudioProductionLine
	if err := db.DB.Where("project_id = ?", projectID).Order("sort_order asc, id asc").Find(&lines).Error; err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("当前项目还没有可导出的台词行")
	}

	missingAudio := make([]string, 0)
	for idx, line := range lines {
		// Reuse the qwen export readiness check (file exists, non-empty).
		if qwenTTSExportAssetReady(line.GeneratedAudio) {
			continue
		}
		missingAudio = append(missingAudio, fmt.Sprintf("%d", idx+1))
	}
	if len(missingAudio) > 0 {
		return nil, fmt.Errorf("还有 %d 行未生成音频，暂不能导出：第 %s 行", len(missingAudio), strings.Join(missingAudio, "、"))
	}
	return lines, nil
}

func buildAudioProductionExportText(lines []models.AudioProductionLine) string {
	rows := make([]string, 0, len(lines))
	for idx, line := range lines {
		lineNumber := line.SortOrder
		if lineNumber <= 0 {
			lineNumber = idx + 1
		}
		speaker := strings.TrimSpace(line.Speaker)
		if speaker == "" {
			rows = append(rows, fmt.Sprintf("%d:%s", lineNumber, strings.TrimSpace(line.Text)))
		} else {
			rows = append(rows, fmt.Sprintf("%d-%s:%s", lineNumber, speaker, strings.TrimSpace(line.Text)))
		}
	}
	return strings.Join(rows, "\n")
}

func buildAudioProductionExportArchive(lines []models.AudioProductionLine, zipPath string) error {
	zipFile, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer zipFile.Close()

	zipWriter := zip.NewWriter(zipFile)
	defer zipWriter.Close()

	for idx, line := range lines {
		lineNumber := line.SortOrder
		if lineNumber <= 0 {
			lineNumber = idx + 1
		}
		sourcePath, err := assetWebPathToAbs(line.GeneratedAudio)
		if err != nil {
			return err
		}
		ext := filepath.Ext(sourcePath)
		if ext == "" {
			ext = ".mp3"
		}
		if err := addFileToZip(zipWriter, fmt.Sprintf("%d%s", lineNumber, ext), sourcePath); err != nil {
			return err
		}
	}

	if err := addTextToZip(zipWriter, "all.txt", buildAudioProductionExportText(lines)); err != nil {
		return err
	}
	return nil
}

// ExportAudioProductionProjectArchive zips all generated audio lines + an all.txt
// manifest, mirroring the Qwen3 TTS export. Closes the audio-production
// "last-mile" gap (it previously had no export).
func ExportAudioProductionProjectArchive(c *gin.Context) {
	project, err := loadAudioProductionProjectOr404(c)
	if err != nil {
		return
	}

	lines, err := listAudioProductionLinesForExport(project.ID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	workspaceDir, err := createTemporaryVideoExportWorkspace(fmt.Sprintf("audio_production_%d_export_", project.ID))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("导出空间创建失败: %v", err)})
		return
	}

	filenameBase := sanitizeQwenTTSExportFilename(project.Code)
	if filenameBase == "未命名" {
		filenameBase = fmt.Sprintf("audio_production_%d", project.ID)
	}
	zipPath := filepath.Join(workspaceDir, fmt.Sprintf("%s_export.zip", filenameBase))
	if err := buildAudioProductionExportArchive(lines, zipPath); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("构建导出压缩包失败: %v", err)})
		return
	}

	c.FileAttachment(zipPath, filepath.Base(zipPath))
}
