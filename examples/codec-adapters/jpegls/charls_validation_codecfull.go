//go:build codecfull && (darwin || linux || windows)

package jpegls

func validateLoadedCharLSAPI(api *charlsAPI) error {
	if err := validateCharLSAPI(api); err != nil {
		return err
	}
	return validateQualifiedCharLSAPI(api)
}
