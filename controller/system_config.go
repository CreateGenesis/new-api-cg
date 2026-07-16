package controller

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func ExportSystemConfig(c *gin.Context) {
	configFile, err := service.ExportSystemConfig()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	data, err := common.Marshal(configFile)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	recordManageAudit(c, "system_config.export", map[string]interface{}{
		"options":  len(configFile.Options),
		"vendors":  len(configFile.Vendors),
		"models":   len(configFile.Models),
		"channels": len(configFile.Channels),
	})
	filename := "system-config-" + time.Now().Format("20060102-150405") + ".json"
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.Data(http.StatusOK, "application/json; charset=utf-8", data)
}

func PreviewSystemConfigImport(c *gin.Context) {
	data, err := readSystemConfigImportBody(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	preview, err := service.PreviewSystemConfigImport(data)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}

	recordManageAudit(c, "system_config.import_preview", systemConfigAuditParams(preview))
	common.ApiSuccess(c, preview)
}

func ApplySystemConfigImport(c *gin.Context) {
	data, err := readSystemConfigImportBody(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	preview, err := service.ApplySystemConfigImport(data, c.Query("preview_hash"))
	if err != nil {
		status := http.StatusBadRequest
		if preview != nil && preview.HasConflicts() {
			status = http.StatusConflict
		}
		c.JSON(status, gin.H{"success": false, "message": err.Error(), "data": preview})
		return
	}

	recordManageAudit(c, "system_config.import", systemConfigAuditParams(preview))
	common.ApiSuccess(c, preview)
}

func readSystemConfigImportBody(c *gin.Context) ([]byte, error) {
	reader := http.MaxBytesReader(c.Writer, c.Request.Body, service.SystemConfigMaxImportSize+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read import file: %w", err)
	}
	if len(data) > service.SystemConfigMaxImportSize {
		return nil, fmt.Errorf("import file exceeds %d MiB", service.SystemConfigMaxImportSize>>20)
	}
	return data, nil
}

func systemConfigAuditParams(preview *service.SystemConfigImportPreview) map[string]interface{} {
	return map[string]interface{}{
		"hash":      preview.Hash,
		"options":   preview.Options.Add + preview.Options.Update,
		"vendors":   preview.Vendors.Add + preview.Vendors.Update,
		"models":    preview.Models.Add + preview.Models.Update,
		"channels":  preview.Channels.Add + preview.Channels.Update,
		"warnings":  len(preview.Warnings),
		"conflicts": len(preview.Conflicts),
	}
}
