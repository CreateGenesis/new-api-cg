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

func ExportSystemBackup(c *gin.Context) {
	backup, err := service.ExportSystemBackup()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	data, err := common.Marshal(backup)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	recordCount := systemBackupRecordCount(backup)
	recordManageAudit(c, "system_backup.export", map[string]interface{}{
		"records": recordCount,
	})
	filename := "system-backup-" + time.Now().Format("20060102-150405") + ".json"
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, "application/json; charset=utf-8", data)
}

func PreviewSystemBackupImport(c *gin.Context) {
	data, err := readSystemBackupImportBody(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	preview, err := service.PreviewSystemBackupImport(data)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	recordManageAudit(c, "system_backup.import_preview", map[string]interface{}{
		"hash":    preview.Hash,
		"records": preview.RecordCount,
	})
	common.ApiSuccess(c, preview)
}

func ApplySystemBackupImport(c *gin.Context) {
	data, err := readSystemBackupImportBody(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	preview, err := service.ApplySystemBackupImport(data, c.Query("preview_hash"))
	if err != nil {
		status := http.StatusBadRequest
		if preview != nil && preview.HasConflicts() {
			status = http.StatusConflict
		}
		c.JSON(status, gin.H{"success": false, "message": err.Error(), "data": preview})
		return
	}
	recordManageAudit(c, "system_backup.import", map[string]interface{}{
		"hash":    preview.Hash,
		"records": preview.RecordCount,
	})
	common.ApiSuccess(c, preview)
}

func readSystemBackupImportBody(c *gin.Context) ([]byte, error) {
	reader := http.MaxBytesReader(c.Writer, c.Request.Body, service.SystemBackupMaxImportSize+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read import file: %w", err)
	}
	if len(data) > service.SystemBackupMaxImportSize {
		return nil, fmt.Errorf("import file exceeds %d MiB", service.SystemBackupMaxImportSize>>20)
	}
	return data, nil
}

func systemBackupRecordCount(backup *service.SystemBackupFile) int {
	if backup == nil {
		return 0
	}
	return len(backup.Options) + len(backup.Channels) + len(backup.Vendors) + len(backup.Models) +
		len(backup.PrefillGroups) + len(backup.Setups) + len(backup.CustomOAuthProviders) +
		len(backup.SubscriptionPlans) + len(backup.AuthorizationRoles) + len(backup.AuthorizationRules) +
		len(backup.Users) + len(backup.Tokens) + len(backup.Redemptions) + len(backup.TwoFA) +
		len(backup.TwoFABackupCodes) + len(backup.Passkeys) + len(backup.ExternalIdentities) +
		len(backup.OAuthBindings) + len(backup.UserSubscriptions)
}
