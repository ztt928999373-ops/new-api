// Copyright (c) QuantumNous and contributors.
package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service/productcatalog"
	"github.com/gin-gonic/gin"
	"time"
)

// GetProductCatalog exposes only the catalog's explicit public projection.
func GetProductCatalog(c *gin.Context) {
	catalog, err := productcatalog.LoadEmbedded(common.Unmarshal, time.Now().UTC())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": productcatalog.PublicView(catalog)})
}
