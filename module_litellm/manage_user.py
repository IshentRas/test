#!/usr/bin/env python3
"""
LiteLLM User Management Script
Combines user creation and key rotation for Terraform external data source.
Reads JSON query from stdin: {"username": "...", "email": "..."}
"""

import urllib.request
import urllib.parse
import urllib.error
import json
import os
import sys
import time

class LiteLLMManager:
    """Manages LiteLLM users and API keys."""

    def __init__(self, proxy_url=None, master_key=None):
        self.proxy_url = proxy_url or os.getenv("LITELLM_PROXY_URL", "http://localhost:4000")
        self.master_key = master_key or os.getenv("LITELLM_MASTER_KEY")

        if not self.master_key:
            raise ValueError("LITELLM_MASTER_KEY not found in environment")

        self.headers = {
            "Authorization": f"Bearer {self.master_key}",
            "Content-Type": "application/json"
        }

    def _make_request(self, url, method="GET", data=None):
        """Make HTTP request to LiteLLM proxy"""
        if data:
            data = json.dumps(data).encode('utf-8')
        req = urllib.request.Request(url, data=data, headers=self.headers, method=method)
        try:
            with urllib.request.urlopen(req) as response:
                return response.getcode(), response.read().decode('utf-8')
        except urllib.error.HTTPError as e:
            return e.code, e.read().decode('utf-8')
        except urllib.error.URLError as e:
            raise Exception(f"URL Error: {e.reason}")

    def get_user_by_email(self, user_email):
        """Get user info by user_email using /user/list endpoint"""
        params = urllib.parse.urlencode({"user_email": user_email})
        status, response_data = self._make_request(f"{self.proxy_url}/user/list?{params}", method="GET")

        if status == 200:
            response = json.loads(response_data)
            users = response.get("users", [])

            # Find user with matching email
            for user in users:
                if user.get("user_email") == user_email:
                    return True, user

            # If users list is empty or no match found
            return False, None

        # If endpoint returns error, assume user doesn't exist
        return False, None

    def create_user(self, username, user_email):
        """Create a new user with a key"""
        if not user_email:
            raise ValueError("user_email is required")

        user_data = {
            "user_email": user_email,
            "user_alias": username,
            "key_alias": f"{username}_key",
            "send_invite_email": False,
            "user_role": "internal_user"
        }

        status, response_data = self._make_request(f"{self.proxy_url}/user/new", method="POST", data=user_data)

        if status != 200:
            raise Exception(f"Failed to create user: {response_data}")

        result = json.loads(response_data)
        key = result.get("key")
        if not key:
            raise Exception("No key returned from user creation")
        return key

    def rotate_key(self, key_alias):
        """Rotate an existing key by alias"""
        # Get key string from /key/list
        params = urllib.parse.urlencode({"key_alias": key_alias})
        status, response_data = self._make_request(f"{self.proxy_url}/key/list?{params}", method="GET")

        if status != 200:
            raise Exception(f"Failed to find key: {response_data}")

        keys = json.loads(response_data).get("keys", [])
        if not keys:
            raise Exception(f"Key with alias '{key_alias}' not found")

        old_key = keys[0]

        # Get full key details
        status, response_data = self._make_request(f"{self.proxy_url}/key/info?key={old_key}", method="GET")
        if status != 200:
            raise Exception(f"Failed to get key details: {response_data}")

        old_key_info = json.loads(response_data).get("info", {})
        user_id = old_key_info.get("user_id")

        if not user_id:
            raise Exception("Missing user_id")

        # Create new key with temporary alias
        temp_alias = f"{key_alias}_temp_{int(time.time())}"
        new_key_data = {
            "user_id": user_id,
            "key_alias": temp_alias,
            "models": old_key_info.get("models", []),
            "max_budget": old_key_info.get("max_budget"),
            "metadata": old_key_info.get("metadata", {}),
            "permissions": old_key_info.get("permissions", {}),
            "tpm_limit": old_key_info.get("tpm_limit"),
            "rpm_limit": old_key_info.get("rpm_limit"),
        }
        new_key_data = {k: v for k, v in new_key_data.items() if v is not None}

        status, response_data = self._make_request(f"{self.proxy_url}/key/generate", method="POST", data=new_key_data)
        if status != 200:
            raise Exception(f"Failed to create new key: {response_data}")

        new_key = json.loads(response_data).get("key")
        if not new_key:
            raise Exception("No key returned")

        # Delete old key
        status, response_data = self._make_request(f"{self.proxy_url}/key/delete", method="POST", data={"keys": [old_key]})
        if status != 200:
            # Warning only - continue
            pass

        # Update new key alias
        status, response_data = self._make_request(f"{self.proxy_url}/key/update", method="POST", data={"key": new_key, "key_alias": key_alias})
        if status != 200:
            raise Exception(f"Failed to update alias: {response_data}")

        return new_key

    def ensure_user(self, username, user_email):
        """Ensure user exists, create if not, rotate key if exists"""
        if not user_email:
            raise ValueError("user_email is required")

        # First try to find user by email
        exists, user_info = self.get_user_by_email(user_email)

        if exists:
            # User exists - get user_alias and rotate the key
            user_alias = user_info.get("user_alias")
            if not user_alias:
                raise Exception("User exists but has no user_alias")

            key_alias = f"{user_alias}_key"
            key = self.rotate_key(key_alias)
            return {
                "action": "rotated",
                "key": key,
                "status": "completed"
            }

        # User doesn't exist - create it
        key = self.create_user(username, user_email)
        return {
            "action": "created",
            "key": key,
            "status": "completed"
        }

def main():
    """
    Main entry point for Terraform external data source.

    Reads JSON query from stdin and manages LiteLLM user.
    Always exits with 0 and returns status in JSON for graceful error handling.
    """
    try:
        stdin_input = sys.stdin.read()
        if not stdin_input:
            result = {
                "action": "failed",
                "key": "",
                "status": "failed",
                "message": "No input received from stdin"
            }
            print(json.dumps(result))
            sys.exit(0)

        query = json.loads(stdin_input)
        username = query.get("username")
        user_email = query.get("email")

        if not username or not user_email:
            result = {
                "action": "failed",
                "key": "",
                "status": "failed",
                "message": "username and email are required in query"
            }
            print(json.dumps(result))
            sys.exit(0)

        manager = LiteLLMManager()
        result = manager.ensure_user(username, user_email)
        # Ensure all values are strings for Terraform compatibility
        terraform_result = {
            "action": str(result.get("action", "")),
            "key": str(result.get("key", "")),
            "status": str(result.get("status", ""))
        }
        print(json.dumps(terraform_result))
        sys.exit(0)
    except json.JSONDecodeError as e:
        result = {
            "action": "failed",
            "key": "",
            "status": "failed",
            "message": f"Invalid JSON input: {str(e)}"
        }
        print(json.dumps(result))
        sys.exit(0)
    except Exception as e:
        result = {
            "action": "failed",
            "key": "",
            "status": "failed",
            "message": str(e)
        }
        print(json.dumps(result))
        sys.exit(0)  # Always exit 0, let Terraform check the status field

if __name__ == "__main__":
    main()
