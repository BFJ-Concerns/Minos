package rangecheck

func Contains(value, minimum, maximum int) bool {
	return value >= minimum && value <= maximum
}
