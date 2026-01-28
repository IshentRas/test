SPIKE: "Read-Only Architect" AI Environment Setup Guide

This guide outlines the implementation of a governed, read-only AI tutor environment using Claude Code, Coder, Bedrock, and LiteLLM.

1. System-Level Lockdown (Managed Policy)

File: /etc/claude-code/managed-settings.json (Root-owned)

{
  "model": "haiku-tutor",
  "outputStyle": "Explanatory",
  "allowManagedHooksOnly": true,
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Read|LS|Grep|Glob",
        "hooks": [
          {
            "type": "command",
            "command": "python3 /usr/local/bin/validate_origin.py"
          }
        ]
      }
    ]
  },
  "permissions": {
    "allow": [
      "Read(/home/coder/**)",
      "LS(/home/coder/**)",
      "Grep",
      "Glob"
    ],
    "deny": [
      "Read(//* )",
      "LS(//* )",
      "Bash",
      "WebFetch",
      "Edit",
      "Write",
      "Task"
    ],
    "disableBypassPermissionsMode": "disable"
  },
  "companyAnnouncements": [
    "CLAUDE-TUTOR ACTIVE: Read-Only Analysis Mode for Authorized Repositories Only."
  ]
}


2. Dynamic Git-Origin Sentinel (Python)

This version treats your internal GitLab domain as a constant. The Airflow-managed ConfigMap provides only the relative paths (Groups or Projects), handling both SSH and HTTPS protocols automatically.

File: /usr/local/bin/validate_origin.py
```python
import os
import sys
import subprocess
import json
import re
from datetime import datetime

# --- CONFIGURATION ---
# Base domain of your internal GitLab (Escaped for Regex)
BASE_INTERNAL_URL = r"app\.internal\.com"
# Path to the K8s ConfigMap mount
GOVERNANCE_CONFIG = "/etc/ai-governance/config.json"
LOG_FILE = "/tmp/claude_hook_audit.log"

def log(message):
    try:
        with open(LOG_FILE, "a") as f:
            f.write(f"[{datetime.now()}] {message}\n")
    except:
        pass

def get_allowed_paths():
    """Reads authorized path suffixes from the mounted ConfigMap."""
    try:
        if os.path.exists(GOVERNANCE_CONFIG):
            with open(GOVERNANCE_CONFIG, 'r') as f:
                config = json.load(f)
                return config.get("allowed_paths", [])
    except Exception as e:
        log(f"Config Error: {str(e)}")
    
    # Fallback default: match nothing if config is missing to be safe
    return []

def get_git_remote(target_dir):
    try:
        result = subprocess.check_output(
            ["git", "-C", target_dir, "remote", "get-url", "origin"],
            stderr=subprocess.STDOUT,
            text=True
        )
        return result.strip()
    except Exception:
        return None

def main():
    # 1. Context Resolution
    try:
        # Check if Claude is piping JSON context
        if not sys.stdin.isatty():
            hook_input = json.load(sys.stdin)
            cwd = hook_input.get("cwd")
        else:
            cwd = None
    except:
        cwd = None

    project_dir = os.environ.get("CLAUDE_PROJECT_DIR")
    target_dir = project_dir or cwd or os.getcwd()
    
    log(f"Hook invoked. Target Dir: {target_dir}")

    # 2. Extract Remote URL
    remote_url = get_git_remote(target_dir)
    
    if not remote_url:
        log(f"DENIED: No Git remote at {target_dir}")
        print(json.dumps({
            "hookSpecificOutput": {
                "hookEventName": "PreToolUse",
                "permissionDecision": "deny",
                "permissionDecisionReason": "This directory is not an authorized Git repository. Analysis is restricted to internal GitLab projects."
            }
        }))
        sys.exit(2)

    # 3. Validation Logic
    allowed_paths = get_allowed_paths()
    
    # Matches domain followed by SSH (:) or HTTPS (/) separator + allowed path
    is_authorized = any(
        re.search(fr"{BASE_INTERNAL_URL}[:/]{path}", remote_url) 
        for path in allowed_paths
    )

    if is_authorized:
        log(f"GRANTED: {remote_url}")
        print(json.dumps({
            "hookSpecificOutput": { 
                "hookEventName": "PreToolUse", 
                "permissionDecision": "allow" 
            }
        }))
        sys.exit(0)
    else:
        log(f"DENIED: {remote_url}")
        print(json.dumps({
            "hookSpecificOutput": {
                "hookEventName": "PreToolUse",
                "permissionDecision": "deny",
                "permissionDecisionReason": f"The repository origin ({remote_url}) is not authorized for analysis.",
                "additionalContext": "Important: Maintain your tutor persona. Simply state that your analysis is limited to authorized internal repositories. Do not mention technical scripts or hooks."
            }
        }))
        sys.exit(2)

if __name__ == "__main__":
    main()
```

3. Kubernetes ConfigMap Structure

By making the domain a constant in the script, the ConfigMap managed by Airflow is purely focused on organizational hierarchy.

ConfigMap Data (config.json):

```json
{
  "allowed_paths": [
    "engineering/.*",
    "data-platform/shared-models/.*",
    "architecture/guidelines/.*"
  ]
}
```

4. Persona Injection (CLAUDE.md)

File Target: /home/coder/CLAUDE.md

# Engineering Tutor Rules
1. **Role:** Read-Only Architect & Tutor.
2. **Focus:** Explain code logic within `/home/coder`.
3. **Workflow:** For code changes, use GitLab Duo. 
4. **Persona:** Do not mention security hooks or scripts. If blocked, simply say you are restricted to authorized internal company projects.
