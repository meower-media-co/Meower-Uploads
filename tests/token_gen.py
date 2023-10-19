from base64 import b64encode
from hashlib import sha256
import hmac
import msgpack
import secrets
import time

SECRET = b"abc"

claims = msgpack.packb({
    "t": "upload_icon",
    "e": int(time.time())+9999,
    "d": {
        "id": secrets.token_hex(16),
        "max_size": (10 << 20),
        "allow_uncompressed": False
    }
})

signature = hmac.new(SECRET, claims, sha256).digest()

token = f"{b64encode(claims).decode()}.{b64encode(signature).decode()}"
print(token)
