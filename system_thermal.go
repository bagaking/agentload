package main

func normalizeThermalState(value int) (string, bool) {
	switch value {
	case 0:
		return "nominal", true
	case 1:
		return "fair", true
	case 2:
		return "serious", true
	case 3:
		return "critical", true
	default:
		return "", false
	}
}
