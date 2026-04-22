import base64
import json
import os

import boto3
from botocore.auth import SigV4Auth
from botocore.awsrequest import AWSRequest


def _b64(data: str) -> str:
    return base64.b64encode(data.encode("utf-8")).decode("utf-8")


def handler(event, context):
    region = os.environ.get("AWS_REGION", "us-east-1")
    server_id = os.environ.get("VAULT_AWS_IAM_SERVER_ID", "").strip()
    # Let botocore endpoint resolution honor AWS_STS_REGIONAL_ENDPOINTS.
    os.environ.setdefault("AWS_STS_REGIONAL_ENDPOINTS", "regional")
    session = boto3.session.Session(region_name=region)
    sts_client = session.client("sts", region_name=region)
    sts_endpoint = sts_client.meta.endpoint_url

    body = "Action=GetCallerIdentity&Version=2011-06-15"
    headers = {"Content-Type": "application/x-www-form-urlencoded; charset=utf-8"}
    if server_id:
        headers["X-Vault-AWS-IAM-Server-ID"] = server_id

    request = AWSRequest(method="POST", url=sts_endpoint, data=body, headers=headers)
    credentials = session.get_credentials().get_frozen_credentials()
    SigV4Auth(credentials, "sts", region).add_auth(request)

    signed_headers = dict(request.headers)

    # Optional network check to prove the regional endpoint is reachable.
    identity = sts_client.get_caller_identity()

    response = {
        "iam_http_request_method": "POST",
        "iam_request_url": _b64(sts_endpoint),
        "iam_request_body": _b64(body),
        "iam_request_headers": _b64(json.dumps(signed_headers)),
        "sts_endpoint_used": sts_endpoint,
        "aws_sts_regional_endpoints": os.environ.get("AWS_STS_REGIONAL_ENDPOINTS", ""),
        "caller_identity": identity,
    }

    return {
        "statusCode": 200,
        "headers": {"Content-Type": "application/json"},
        "body": json.dumps(response),
    }
