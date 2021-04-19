package versiondata

const (
	defaultProduct = "datalake.speedboat.seagate.com"
	defaultVersion = "v0-dbg"
)

var (
	Product   = defaultProduct
	Version   = defaultVersion
	BuildSHA  string
	BuildTime string
)
