import sys

file_path = 'pkg/server/server.go'
with open(file_path, 'r', encoding='utf-8') as f:
    content = f.read()

old_routes = """	group := router.Group("/")
	c.routes(group)"""

new_routes = """	group := router.Group("/")
	c.routes(group)
	c.adminRoutes(group)"""

content = content.replace(old_routes, new_routes)
content = content.replace(old_routes.replace('\n', '\r\n'), new_routes)

with open(file_path, 'w', encoding='utf-8') as f:
    f.write(content)
print("Patched server.go routes")
