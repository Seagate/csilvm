package versiondata

const (
	defaultProduct = "datalake.speedboat.seagate.com"
	defaultVersion = "v0-dev"
)

var (
	Product   = defaultProduct
	Version   = defaultVersion
	BuildSHA  string
	BuildTime string
)
