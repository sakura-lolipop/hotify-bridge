import urllib.request, json
TOKEN = "KAAAAACy1m7FAABzsQuL3Yfq0G2QhG6VlTjogTBdXlNtY46d7JPi59VueHBYrA54FDj9pi0xCE7pNOvAP2Jfl8yNV_ZIr1Z8LZXE1Zng3YqA3g"
p = {"validate_only": False, "message": {"android": {"notification": {"title": "Hotify", "body": "x", "click_action": {"type": 3}}}, "token": [TOKEN]}}
req = urllib.request.Request("http://127.0.0.1:9876/p", data=json.dumps(p).encode(), headers={"Content-Type": "application/json", "Authorization": "Bearer TT"}, method="POST")
urllib.request.urlopen(req).read()
