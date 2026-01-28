SPIKE: "Read-Only Architect" AI Environment Setup GuideThis guide outlines the implementation of a governed, read-only AI tutor environment using Claude Code, Coder, Bedrock, and LiteLLM.1. System-Level Lockdown (Managed Policy)This file must be created by an administrator (root) within the Coder image to ensure it cannot be modified by the user.File: /etc/claude-code/managed-settings.json
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
            "command": "/usr/local/bin/validate-origin.sh"
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
2. Git-Origin Sentinel (Validation Script)This script is triggered on every tool call to verify that the project Claude is interacting with belongs to your organization.File: /usr/local/bin/validate-origin.sh#!/bin/bash
# Hook context: CLAUDE_PROJECT_DIR is provided by Claude Code

# 1. Get the remote URL of the current project
REMOTE_URL=$(git -C "$CLAUDE_PROJECT_DIR" remote get-url origin 2>/dev/null)

# 2. Define the allowed GitLab workspace/group pattern
# Change this to match your GitLab organization
ALLOWED_PATTERN="gitlab.com[:/]your-company-workspace"

# 3. Validation Logic
if [[ "$REMOTE_URL" =~ $ALLOWED_PATTERN ]]; then
    exit 0 # Path Authorized
else
    # This message is displayed directly to the user in the terminal
    echo "SECURITY ALERT: Repository ($REMOTE_URL) is NOT authorized for AI analysis." >&2
    exit 1 # Blocks the tool execution
fi
Action Required: sudo chmod +x /usr/local/bin/validate-origin.sh3. Persona Injection (Kubernetes ConfigMap)Mount this file into the workspace using a Kubernetes ConfigMap with a subPath to ensure it is persistent and read-only.File Target: /home/coder/CLAUDE.md# Engineering Tutor Rules
1. **Role:** You are a Read-Only Software Architect and Tutor.
2. **Focus:** Only analyze and explain code logic within `/home/coder`.
3. **Restriction:** You cannot write or edit files. You cannot execute bash commands.
4. **Workflow:** Explain the "Why" and the "How." For actual code implementation or bug fixes, direct the user to use **GitLab Duo**.
5. **Scope:** Only discuss approved company repositories as validated by your internal security hooks.
4. Coder Environment ConfigurationEnsure your Coder workspace exports the following variables (likely via your custom LiteLLM virtual key module):export ANTHROPIC_API_KEY="sk-your-litellm-virtual-key"
export ANTHROPIC_BASE_URL="[https://your-litellm-proxy.internal/v1](https://your-litellm-proxy.internal/v1)"
export ANTHROPIC_MODEL="haiku-tutor"
5. SPIKE Verification Protocol (Acid Tests)Test 1: The "Imposter" Repo (Security Check)Action: git clone https://github.com/django/django (or any public repo).Action: Run claude "Explain this project"Expected Result: The validate-origin.sh hook should trigger, and Claude should report a Security Alert denying access.Test 2: The "Hands-Tied" Test (Permission Check)Action: In an authorized repo, ask Claude: "Delete the README.md" or "Create a new file named test.py".Expected Result: Claude should state it does not have permission to use the Write or Bash tools.Test 3: The "Tutor Style" Test (Persona Check)Action: Ask Claude: "How does the database connection logic work?"Expected Result: Claude should provide a narrative explanation (due to Explanatory mode) and explicitly mention that you should use GitLab Duo if you want to modify it.Test 4: The "Budget Hard-Stop" (Financial Check)Action: Monitor the LiteLLM dashboard for the specific virtual key.Expected Result: Verify that spend is correctly attributed to that user/key. (Manually lower a test key's budget to $0.01 to verify the 429 error handling).Next Steps: Once these tests are validated, we will proceed to draft the Executive Summary focusing on Cognitive Safety, Financial Control, and IP Protection.
