import sys

file_path = 'pkg/server/web/admin.html'
with open(file_path, 'r', encoding='utf-8') as f:
    content = f.read()

content = content.replace("item.name.toLowerCase()", "item.category_name.toLowerCase()")
content = content.replace("item.name", "item.category_name")

with open(file_path, 'w', encoding='utf-8') as f:
    f.write(content)
print("Fixed JS variable names")
