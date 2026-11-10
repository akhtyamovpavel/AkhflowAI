package store

func CanReadProject(role string) bool {
	switch role {
	case "owner", "admin", "member", "editor", "viewer":
		return true
	default:
		return false
	}
}

func CanWriteProject(role string) bool {
	switch role {
	case "owner", "admin", "member", "editor":
		return true
	default:
		return false
	}
}
