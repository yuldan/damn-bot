package whatsapp_cloud

// Secret names the Cloud API descriptor declares. `damnbot whatsapp-cloud
// setup` records these references in config; the values stay in the keychain
// and are read through the gwplat secret seam at runtime.
const (
	AccessTokenSecret = "whatsapp_cloud_access_token" // #nosec G101 -- secret name, not a value
	AppSecretSecret   = "whatsapp_cloud_app_secret"   // #nosec G101 -- secret name, not a value
	VerifyTokenSecret = "whatsapp_cloud_verify_token" // #nosec G101 -- secret name, not a value
)
