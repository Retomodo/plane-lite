# Generates internal/drf/testdata/fixtures.json from the reference stack:
#   docker compose -f docker-compose.dev.yml exec -T reference python manage.py shell < contract/reference/gen_drf_fixtures.py
import json, uuid, zoneinfo
from django.utils.dateparse import parse_datetime
from django.core.validators import URLValidator
from django.core.exceptions import ValidationError

dts = ["2026-10-07T10:00:00", "2026-10-07 10:00:00.5+01:00", "2026-10-07T10:00Z", "2026-10-07", "20261007T100000",
    "2026-10-07T10", "2026-10-07T10:00:00+0530", "2026-10-07T10:00:00,5", "2026-10-07T10:00:00.1234567", "2026-W41-3",
    "2026W413", "2026W41", "2026-W41", "2026-W53-1", "2020-W53-7", "2026-10-07T24:00:00", "2026-10-07T10:00:00 +01:00",
    "2026-1-7 3:04", "2026-10-07T10:00:00-00:30:15", " 2026-10-07T10:00:00", "2026-13-07T10:00:00", "2026-10-07T10:00:00.",
    "2026-10-07T10:00:00x+01:00", "2026-10-07T10:00.5", "2026-10-07T1000", "2026-10-07T100000.5Z", "2026-10-07T10:00:00Zx",
    "2026-10-07T10:00:00+05", "2026-10-07T10:00:00+053", "2026-10-07T10:00:00+05:30:15.25", "2026-10-07T10:00:00+24:00",
    "2026-10-07T10:00:00+23:59", "2026-10-07T10:00:00-00:00", "2026-10-07X10:00", "2026-10-07é10:00", "2026-02-30",
    "2024-02-29T00:00", "0000-01-01", "2026-10-07T10:00:00\n", "2026-10-07 10:00 Z", "2026-10-07 10:00:00.1234567890",
    "2026-10-07T10:61", "2026-10-07T", "2026-10-0", "2026107", "2026-10-07T10:00:00.000001", "2026-10-07T10:00:60",
    "2026-10-07 10:00+0100", "2026-10-07 10:00 +01", "2026-10-07T10-01:00", "2026-10-07T10:00:00.5-03:00"]
tz = zoneinfo.ZoneInfo("Asia/Kolkata")
out = {"datetimes": [], "urls": [], "floats": [], "uuids": []}
for s in dts:
    try:
        d = parse_datetime(s)
    except ValueError:
        d = None
    if d is not None and d.tzinfo is None:
        d = d.replace(tzinfo=tz)
    try:
        r = d.astimezone(zoneinfo.ZoneInfo("UTC")).isoformat() if d else None
    except Exception:
        r = None
    out["datetimes"].append([s, r])
urls = ["https://images.unsplash.com/photo-1?w=1200&q=80", "ftp://files.example.com/a.png", "javascript://example.com/%0a",
    "http://[::1]:8080/x", "http://[::zz]/x", "http://localhost:3000/a b", "https://-bad-.com/", "http://example", "http://localhost",
    "http://127.0.0.1", "http://256.1.1.1", "http://user:pw@example.com:80/p?q#f", "HTTP://EXAMPLE.COM", "https://例え.テスト/",
    "http://xn--fsqu00a.xn--zckzah/", "http://a.b-.com", "http://ex ample.com", "http://example.com.", "http://.example.com",
    "http://[1.2.3.4]/", "http://[::ffff:1.2.3.4]/", "http://[v1.fe]/", "http://example.com:999999", "http://example.com:65536",
    "mailto://a@b.com", "http://" + "a" * 64 + ".com", "http://" + ".".join(["a" * 60] * 5) + ".com", "http://exa_mple.com",
    "https://www.example.co.uk/path/to?x=1", "http://[2001:db8::1]:443", "http://[2001:db8::1", "http://example.com/ x",
    "http://e.c", "http://example.c0m", "http://1.2.3", "http://localhost.localdomain"]
v = URLValidator()
for u in urls:
    try:
        v(u); ok = True
    except ValidationError:
        ok = False
    out["urls"].append([u, ok])
for f in [1.0, 0.1, 1e16, 1e15, 123456789012345678.0, 1.5e-5, 0.0001, 0.00001, -2.5, 3.14159, 1e300, -0.0, 100.0, 2.5e-10]:
    out["floats"].append([f, repr(f)])
for s in ["0B6D8F0E1C514C559A4E6F3F1D6C3D01", "{0b6d8f0e-1c51-4c55-9a4e-6f3f1d6c3d01}", "urn:uuid:0b6d8f0e-1c51-4c55-9a4e-6f3f1d6c3d01",
    "0b6d-8f0e1c514c559a4e6f3f1d6c3d01", "nope", "0b6d8f0e1c514c559a4e6f3f1d6c3d0", "+b6d8f0e1c514c559a4e6f3f1d6c3d01"]:
    try:
        out["uuids"].append([s, str(uuid.UUID(hex=s))])
    except ValueError:
        out["uuids"].append([s, None])
print("FIXTURES" + json.dumps(out, ensure_ascii=False))
