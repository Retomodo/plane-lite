# Prints the SQL the Django reference runs for each request, to port query
# structure (joins, DISTINCT, GROUP BY, window functions) exactly. Run it
# through scripts/capture-sql.sh, which copies the request list in.
#
# Request file lines: "<email> <METHOD> <path> [json body]". Data comes from
# whatever the last recorded scenario left in plane_ref.
import json
from django.db import connection
from django.test.utils import CaptureQueriesContext
from rest_framework.test import APIClient
from plane.db.models import User

for line in open("/tmp/capture_requests.txt"):
    line = line.strip()
    if not line or line.startswith("#"):
        continue
    parts = line.split(" ", 3)
    email, method, path = parts[0], parts[1].lower(), parts[2]
    body = json.loads(parts[3]) if len(parts) > 3 else None
    client = APIClient()
    client.force_authenticate(User.objects.get(email=email))
    with CaptureQueriesContext(connection) as ctx:
        resp = getattr(client, method)(path, body, format="json") if body is not None else getattr(client, method)(path)
    print("=====", email, method.upper(), path, resp.status_code)
    for q in ctx.captured_queries:
        print("--", q["sql"])
