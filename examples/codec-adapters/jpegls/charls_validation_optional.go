//go:build jpegls_charls && !codecfull && (darwin || linux || windows)

package jpegls

func validateLoadedCharLSAPI(api *charlsAPI) error {
	return validateCharLSAPI(api)
}
