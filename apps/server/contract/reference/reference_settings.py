# Settings for the Django *reference* server used to record contract goldens.
# Mounted into the Django container as plane/settings/reference.py; never
# shipped. Mirrors production, except Celery tasks run inline so every
# side effect (activity, notifications, seeds) has landed before the HTTP
# response returns -- which keeps recorded responses deterministic.

from .production import *  # noqa

CELERY_TASK_ALWAYS_EAGER = True
CELERY_TASK_EAGER_PROPAGATES = False
