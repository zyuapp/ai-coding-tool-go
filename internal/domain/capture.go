package domain

var DefaultCaptureOptions = CaptureOptions{Sound: true, Focus: true}

func IsCaptureOptions(value any) bool {
	options, ok := record(value)
	if !ok {
		return false
	}
	_, sound := options["sound"].(bool)
	_, focus := options["focus"].(bool)
	return sound && focus
}
