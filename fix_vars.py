import sys

file_path = 'pkg/server/server.go'
with open(file_path, 'r', encoding='utf-8') as f:
    content = f.read()

content = content.replace("config.RemoteURL", "cfgData.RemoteURL")
content = content.replace("config.CustomId", "cfgData.CustomId")
content = content.replace("config.XtreamBaseURL", "cfgData.XtreamBaseURL")
content = content.replace("config.MetadataCacheTTL", "cfgData.MetadataCacheTTL")
content = content.replace("config.XMLTVCacheTTL", "cfgData.XMLTVCacheTTL")

with open(file_path, 'w', encoding='utf-8') as f:
    f.write(content)
print("Fixed variable names")
