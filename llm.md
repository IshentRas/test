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
# Path to the K8s ConfigMap mount (Managed by Airflow)
GOVERNANCE_CONFIG = "/etc/ai-governance/config.json"
HOME_CODER = "/home/coder"
LOG_FILE = "/tmp/claude_hook_audit.log"
DEBUG_INPUT_FILE = "/tmp/claude_last_input.json"

def log(message):
    try:
        # Scrub common token patterns from logs (e.g., glpat-...)
        # Updated to include '.' as glpat tokens often contain them
        scrubbed = re.sub(r'glpat-[\w.-]+', '[REDACTED_TOKEN]', message)
        with open(LOG_FILE, "a") as f:
            f.write(f"[{datetime.now()}] {scrubbed}\n")
    except:
        pass

def scrub_url(url):
    """Removes credentials/tokens from Git URLs for safe echoing."""
    if not url:
        return ""
    # Matches http(s)://user:token@domain or user:token@domain
    # Includes '.' in the token match group
    return re.sub(r'(https?://|git@)?[\w.-]+:[\w.-]+@', r'\1', url)

def get_allowed_paths():
    """Reads authorized path suffixes from the mounted ConfigMap."""
    try:
        if os.path.exists(GOVERNANCE_CONFIG):
            with open(GOVERNANCE_CONFIG, 'r') as f:
                config = json.load(f)
                return config.get("allowed_paths", [])
    except Exception as e:
        log(f"Config Error: {str(e)}")
    return []

def get_git_remote(target_path):
    """Finds the nearest git root for a specific path and returns its origin URL."""
    try:
        curr = target_path
        # Traverse up to find a valid directory
        while curr and not os.path.isdir(curr) and curr != "/":
            curr = os.path.dirname(curr)
        
        search_dir = curr if curr and os.path.isdir(curr) else os.getcwd()
        
        git_root = subprocess.check_output(
            ["git", "-C", search_dir, "rev-parse", "--show-toplevel"],
            stderr=subprocess.STDOUT,
            text=True,
            timeout=2
        ).strip()
        
        result = subprocess.check_output(
            ["git", "-C", git_root, "remote", "get-url", "origin"],
            stderr=subprocess.STDOUT,
            text=True,
            timeout=2
        )
        return result.strip()
    except Exception:
        return None

def main():
    target_path = None
    cwd = os.getcwd()
    tool_name = "Unknown"
    
    try:
        if not sys.stdin.isatty():
            raw_input = sys.stdin.read()
            with open(DEBUG_INPUT_FILE, "w") as f:
                f.write(raw_input)
            
            data = json.loads(raw_input)
            cwd = data.get("cwd", os.getcwd())
            tool_name = data.get("tool_name", "")
            tool_input = data.get("tool_input", {})
            
            raw_path = None
            if tool_name in ["Read", "Write", "Edit", "MultiEdit"]:
                raw_path = tool_input.get("file_path")
            elif tool_name in ["Glob", "Grep", "LS"]:
                # Logic for tools that search or list
                raw_path = tool_input.get("path") or tool_input.get("pattern")
                
                if tool_name == "Glob" and raw_path:
                    # Handle shorthand glob patterns
                    if raw_path.startswith("**/"):
                        raw_path = os.path.join(cwd, raw_path[3:])
                    
                    # Strip wildcards to resolve a base path for Git validation
                    raw_path = re.sub(r'[*?\[].*', '', raw_path)

            # Resolve to Absolute Path: 
            # If the tool provided a relative path (doesn't start with /), anchor it to CWD
            if raw_path:
                if not os.path.isabs(raw_path):
                    target_path = os.path.join(cwd, raw_path)
                else:
                    target_path = raw_path
            else:
                target_path = cwd
        else:
            target_path = os.getcwd()
    except Exception as e:
        log(f"Input Parsing Error: {str(e)}")
        target_path = os.getcwd()

    target_path = os.path.abspath(target_path or os.getcwd())
    log(f"Hook [{tool_name}]: Resolving target {target_path}")

    # 2. FAST FAIL: Path Jail Check
    if not target_path.startswith(HOME_CODER):
        log(f"FAST FAIL: {target_path} is outside of {HOME_CODER}")
        print(json.dumps({
            "hookSpecificOutput": {
                "hookEventName": "PreToolUse",
                "permissionDecision": "deny",
                "permissionDecisionReason": "Access denied. I am restricted to analyzing files within your home directory for security reasons."
            }
        }))
        sys.exit(2)

    # 3. Extract Remote URL
    remote_url = get_git_remote(target_path)
    safe_remote_url = scrub_url(remote_url)
    
    if not remote_url:
        log(f"DENIED: No Git remote found for {target_path}")
        print(json.dumps({
            "hookSpecificOutput": {
                "hookEventName": "PreToolUse",
                "permissionDecision": "deny",
                "permissionDecisionReason": "This directory is not an authorized Git repository. I am only able to provide architectural guidance for authorized corporate projects. (Instructions: Stay in persona as a tutor. Do not mention technical security hooks.)"
            }
        }))
        sys.exit(2)

    # 4. Validation Logic
    allowed_paths = get_allowed_paths()
    is_authorized = any(
        re.search(fr"{BASE_INTERNAL_URL}[:/]{path}", remote_url) 
        for path in allowed_paths
    )

    if is_authorized:
        log(f"GRANTED: {safe_remote_url}")
        print(json.dumps({
            "hookSpecificOutput": { 
                "hookEventName": "PreToolUse", 
                "permissionDecision": "allow" 
            }
        }))
        sys.exit(0)
    else:
        log(f"DENIED: {safe_remote_url}")
        # Only echo back the 'safe' version of the URL to prevent token leaking
        print(json.dumps({
            "hookSpecificOutput": {
                "hookEventName": "PreToolUse",
                "permissionDecision": "deny",
                "permissionDecisionReason": f"The repository at {BASE_INTERNAL_URL} is not authorized for analysis. (Instructions: Maintain your tutor persona. State simply that you are limited to authorized internal projects.)"
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
