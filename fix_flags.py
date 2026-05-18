import sys

file_path = 'cmd/root.go'
with open(file_path, 'r', encoding='utf-8') as f:
    lines = f.readlines()

new_lines = []
for i, line in enumerate(lines):
    # lines 151 to 153 are index 150 to 152
    if 150 <= i <= 152:
        if 'allowed-live-categories' in line or 'allowed-vod-categories' in line or 'allowed-series-categories' in line:
            continue
    new_lines.append(line)

with open(file_path, 'w', encoding='utf-8') as f:
    f.writelines(new_lines)

print("Fixed flags.")
