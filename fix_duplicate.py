import sys

file_path = 'pkg/server/xtreamHandles.go'
with open(file_path, 'r', encoding='utf-8') as f:
    content = f.read()

# The function is duplicated. We can just replace two copies with one.
duplicate = """func isAllowedCategory(categoryName string, allowedPrefixes []string) bool {
	if len(allowedPrefixes) == 0 {
		return true // Allow all if not configured
	}
	nameUpper := strings.ToUpper(categoryName)
	for _, prefix := range allowedPrefixes {
		if strings.HasPrefix(nameUpper, strings.ToUpper(prefix)) {
			return true
		}
	}
	return false
}

func isAllowedCategory(categoryName string, allowedPrefixes []string) bool {
	if len(allowedPrefixes) == 0 {
		return true // Allow all if not configured
	}
	nameUpper := strings.ToUpper(categoryName)
	for _, prefix := range allowedPrefixes {
		if strings.HasPrefix(nameUpper, strings.ToUpper(prefix)) {
			return true
		}
	}
	return false
}"""

single = """func isAllowedCategory(categoryName string, allowedPrefixes []string) bool {
	if len(allowedPrefixes) == 0 {
		return true // Allow all if not configured
	}
	nameUpper := strings.ToUpper(categoryName)
	for _, prefix := range allowedPrefixes {
		if strings.HasPrefix(nameUpper, strings.ToUpper(prefix)) {
			return true
		}
	}
	return false
}"""

content = content.replace(duplicate, single)
content = content.replace(duplicate.replace('\n', '\r\n'), single)

with open(file_path, 'w', encoding='utf-8') as f:
    f.write(content)

print("Fixed duplicate function.")
