#!/usr/bin/env bash
set -euo pipefail
dirs=(ai art contract manager session skills templates)
packaged=$(cargo package -p pgmi-shared --list --allow-dirty | grep -E "^($(IFS='|'; echo "${dirs[*]}"))/" | sort)
tracked=$(cd shared && git ls-files -- "${dirs[@]}" | sort)
diff <(echo "$packaged") <(echo "$tracked")
