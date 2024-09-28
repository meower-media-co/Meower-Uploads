"""
The migration from Postgres to MongoDB was done because of limited infrastructure resources and the need to consolidate databases.

This only works if you are on the latest uploads migration. Please run the last Postgres uploads server before running this.
"""

import pymongo, psycopg2

pg_conn = psycopg2.connect(input("Postgres URI:"))
pg_cur = pg_conn.cursor()
pg_cur.execute("""SELECT
id,
hash,
bucket,
mime,
filename,
width,
height,
upload_region,
uploaded_by,
uploaded_at,
claimed FROM files""")
print("Fetching files from Postgres...")
pg_files = pg_cur.fetchall()
print(f"Got {len(pg_files)} files from Postgres!")

mongo_db = pymongo.MongoClient(input("MongoDB URI:"))[input("MongoDB Database:")]
mongo_ops = []
print("Constructing MongoDB objects...")
for file in pg_files:
    mongo_ops.append(pymongo.InsertOne({
        "_id": file[0],
        "hash": file[1],
        "bucket": file[2],
        "mime": file[3],
        "filename": file[4],
        "width": file[5],
        "height": file[6],
        "upload_region": file[7],
        "uploaded_by": file[8],
        "uploaded_at": file[9],
        "claimed": file[10]
    }))
print("Inserting MongoDB objects...")
mongo_db.files.bulk_write(mongo_ops)

print("Complete!")
