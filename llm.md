SPIKE: "Read-Only Architect" AI Environment Setup Guide

This guide outlines the implementation of a governed, read-only AI tutor environment using Claude Code, Coder, Bedrock, and LiteLLM.

1. System-Level Lockdown (Managed Policy)

This file must be created by an administrator (root) within the Coder image.

Logic: We jail Claude to /home/coder using absolute path permissions (//) and then apply a Python-based Git Sentinel hook to validate the origin of any code being read.

File: /etc/claude-code/managed-settings.json

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


2. Git-Origin Sentinel (Python Implementation)

Using Python provides better JSON handling and more reliable path resolution. Claude Code passes CLAUDE_PROJECT_DIR in the environment, and the full hook context via stdin.

File: /usr/local/bin/validate_origin.py

SPIKE: "Read-Only Architect" AI Environment Setup Guide

This guide outlines the implementation of a governed, read-only AI tutor environment using Claude Code, Coder, Bedrock, and LiteLLM.

1. System-Level Lockdown (Managed Policy)

This file must be created by an administrator (root) within the Coder image.

Logic: We jail Claude to /home/coder using absolute path permissions (//) and then apply a Python-based Git Sentinel hook to validate the origin of any code being read.

File: /etc/claude-code/managed-settings.json

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


2. Git-Origin Sentinel (Python Implementation)

Using Python provides better JSON handling and more reliable path resolution. Claude Code passes CLAUDE_PROJECT_DIR in the environment, and the full hook context via stdin.

File: /usr/local/bin/validate_origin.py

import os
import sys
import subprocess
import json
from datetime import datetime

# --- CONFIGURATION ---
ALLOWED_PATTERN = "gitlab.com[:/]your-company-workspace"
LOG_FILE = "/tmp/claude_hook_audit.log"

def log(message):
    try:
        with open(LOG_FILE, "a") as f:
            f.write(f"[{datetime.now()}] {message}\n")
    except:
        pass

def get_git_remote(target_dir):
    try:
        # Get the remote URL for 'origin'
        result = subprocess.check_output(
            ["git", "-C", target_dir, "remote", "get-url", "origin"],
            stderr=subprocess.STDOUT,
            text=True
        )
        return result.strip()
    except Exception:
        return None

def main():
    # 1. Parse the Hook Input from stdin
    # Claude pipes a JSON object containing session_id, cwd, and tool_input
    try:
        hook_input = json.load(sys.stdin)
        cwd = hook_input.get("cwd")
    except Exception:
        cwd = None

    # Fallback to env or current directory if stdin is empty
    project_dir = os.environ.get("CLAUDE_PROJECT_DIR")
    target_dir = project_dir or cwd or os.getcwd()
    
    log(f"Hook invoked. Target Dir: {target_dir}")

    # 2. Extract Git Remote
    remote_url = get_git_remote(target_dir)
    
    if not remote_url:
        log(f"DENIED: No Git remote found at {target_dir}")
        print(json.dumps({
            "hookSpecificOutput": {
                "hookEventName": "PreToolUse",
                "permissionDecision": "deny",
                "permissionDecisionReason": "This directory is not an authorized Git repository. AI analysis is restricted to corporate GitLab projects."
            }
        }))
        sys.exit(2) # Exit 2 is the standard 'Blocking Error' code

    # 3. Pattern Matching
    if ALLOWED_PATTERN in remote_url:
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
                "permissionDecisionReason": f"The repository origin ({remote_url}) is not authorized for AI analysis. Access is restricted to approved projects."
            }
        }))
        sys.exit(2)

if __name__ == "__main__":
    main()


Action Required:

sudo chmod 755 /usr/local/bin/validate_origin.py

touch /tmp/claude_hook_audit.log && chmod 666 /tmp/claude_hook_audit.log

3. Persona Injection (Kubernetes ConfigMap)

File Target: /home/coder/CLAUDE.md (Read-Only Mount)

# Engineering Tutor Rules
1. **Role:** Read-Only Architect & Tutor.
2. **Focus:** Explain code logic within `/home/coder`.
3. **Workflow:** For code changes, use GitLab Duo. Do not ask for Bash or Write permissions.
4. **Scope:** Only corporate GitLab repositories are approved for analysis.


4. Verification Protocol (Acid Tests)

Test 1: Absolute Path Check

Action: claude "Read /etc/shadow"

Expected Result: Immediate permission error (JSON Deny rule).

Test 2: Git Remote Check

Action: git clone a public repo into /home/coder/external-code.

Action: Run claude "Analyze this repo".

Expected Result: Claude reports the reason from the Python script: "The repository origin is not authorized..."

Test 3: The "/init" Check

Action: Run /init in an empty folder.

Expected Result: Graceful exit with "Not an authorized Git repository" message.
Action Required:

sudo chmod 755 /usr/local/bin/validate_origin.py

touch /tmp/claude_hook_audit.log && chmod 666 /tmp/claude_hook_audit.log

3. Persona Injection (Kubernetes ConfigMap)

File Target: /home/coder/CLAUDE.md (Read-Only Mount)

# Engineering Tutor Rules
1. **Role:** Read-Only Architect & Tutor.
2. **Focus:** Explain code logic within `/home/coder`.
3. **Workflow:** For code changes, use GitLab Duo. Do not ask for Bash or Write permissions.
4. **Scope:** Only corporate GitLab repositories are approved for analysis.


4. Verification Protocol (Acid Tests)

Test 1: Absolute Path Check

Action: claude "Read /etc/shadow"

Expected Result: Immediate permission error (JSON Deny rule).

Test 2: Git Remote Check

Action: git clone a public repo into /home/coder/external-code.

Action: Run claude "Analyze this repo".

Expected Result: Claude reports the reason from the Python script: "The repository origin is not authorized..."

Test 3: The "/init" Check

Action: Run /init in an empty folder.

Expected Result: Graceful exit with "Not an authorized Git repository" message.
