import re,sys,json,time,os
src=open(sys.argv[1],encoding="utf-8").read()
src=src.replace('<link rel="preconnect" href="https://fonts.googleapis.com">\n','')
src=re.sub(r'<link rel="stylesheet" href="https://fonts\.googleapis\.com[^>]*>','<link rel="stylesheet" href="/fonts/fonts.css">',src)
assert "fonts.googleapis" not in src
head='<!doctype html>\n<html lang="fr">\n<head>\n<meta charset="utf-8">\n<meta name="viewport" content="width=device-width, initial-scale=1">\n<link rel="icon" href="/icon.png">\n<script src="/shim.js"></script>\n'
# move <title>/<link>/<style> in page into head is not required; browsers accept them in body
open(sys.argv[2],"w",encoding="utf-8").write(head+"</head>\n<body>\n"+src+"\n</body>\n</html>\n")

# Numéro de version de la page (date de construction), lu par la mise à jour automatique
ver=int(time.strftime("%Y%m%d%H%M"))
open(os.path.join(os.path.dirname(sys.argv[2]),"version.json"),"w").write(json.dumps({"version":ver}))
