# PPT Skill 瀹炴柦鏂规

## 涓€銆佺洰鏍?
灏?PPT 鐢熸垚鑳藉姏灏佽涓?hiq 鐨勭嫭绔?skill 瀹夎鍖呫€傜敤鎴烽€氳繃鑷劧璇█鎸囦护鍗冲彲鐢熸垚涓撲笟 PPT锛屼笉渚濊禆瀹屾暣 ppt-auto 椤圭洰銆?
## 浜屻€佹灦鏋?
```
鐢ㄦ埛锛?鍋氫竴涓崕涓烘櫤绠椾腑蹇冪殑PPT"
    鈫?鍚庣锛歛nalyze_template.py 鈫?瀵煎嚭妯℃澘鑳屾櫙鍥撅紙鏀寔 PowerPoint 鍜?WPS锛?    鈫?妯″瀷锛氳 template_config.json + 鐢ㄦ埛闇€姹?鈫?閫愰〉鎵嬪啓 SVG
    鈫?鍚庣锛歴vg_to_pptx.py 鈫?杞崲 PPTX
    鈫?杈撳嚭缁欑敤鎴?```

**鍘熷垯锛?*
- 妯″瀷鏄璁″笀锛岃嚜鐢卞垱浣滃竷灞€鍜屽唴瀹?- 閰嶇疆鏄璁?brief锛屾彁渚涢粯璁ゅ€硷紝鐢ㄦ埛鍙紪杈?- 鍚庣鍙仛浣撳姏娲伙紙妯℃澘鍒嗘瀽銆佹牸寮忚浆鎹級
- 鐢ㄦ埛鎸囧畾 > 閰嶇疆榛樿鍊?
## 涓夈€佹枃浠剁粨鏋?
```
hiq/skills/ppt-auto/
鈹溾攢鈹€ SKILL.md                    # 娴佺▼鎸囧紩锛堢粰妯″瀷璇伙級
鈹溾攢鈹€ template_config.json        # 榛樿璁捐绾︽潫锛堢敤鎴峰彲缂栬緫锛?鈹溾攢鈹€ templates/
鈹?  鈹斺攢鈹€ 涓浗绉诲姩妯℃澘.pptx        # 榛樿妯℃澘锛堟墦鍖呭唴缃級
鈹溾攢鈹€ scripts/
鈹?  鈹溾攢鈹€ analyze_template.py     # 妯℃澘鍒嗘瀽锛圥owerPoint + WPS锛?鈹?  鈹斺攢鈹€ svg_to_pptx.py          # SVG 杞?PPTX锛堜粠 ppt-auto 鎻愬彇锛?鈹斺攢鈹€ README.md                   # 浣跨敤璇存槑
```

## 鍥涖€佸悇鏂囦欢璁捐

### 4.1 template_config.json

**鏈€灏忓寲绾︽潫锛屼繚鐣欒嚜鐢卞害銆?* 鍙害鏉熷繀瑕佺殑瀵归綈鍜屾樉绀鸿鍒欍€?
```json
{
  "template": "鍐呯疆:涓浗绉诲姩妯℃澘.pptx",
  "template_override": null,
  "slides": { "cover": 1, "content": 2, "ending": 3 },
  "canvas": { "width": 1280, "height": 720 },
  "colors": {
    "primary": "#0070C0",
    "secondary": "#2E8B57",
    "accent": "#FF8C00",
    "text": "#333333",
    "text_secondary": "#666666",
    "card_bg": "#F5F7FA"
  },
  "fonts": {
    "family": "Microsoft YaHei, Arial, sans-serif"
  },
  "rules": {
    "text_in_box": "鏂囧瓧蹇呴』鍦ㄦ浣撳唴閮紝y = box_y + box_h/2 + font_size/3",
    "text_length": "鍗曡涓嶈秴杩?0涓腑鏂囧瓧绗?,
    "forbidden_elements": ["filter", "feDropShadow", "pattern", "mask", "foreignObject"]
  }
}
```

**璁捐鎰忓浘锛?*
- `template_override`: 鐢ㄦ埛濉湰鍦拌矾寰勫悗锛岄粯璁ゆā鏉垮け鏁?- `colors`: 鍙彁渚涘熀纭€鑹诧紝涓嶉檺鍒舵€庝箞鐢?- `fonts`: 鍙寚瀹氬瓧浣撴棌锛屼笉闄愬埗瀛楀彿锛堟ā鍨嬭嚜鐢遍€夋嫨锛?- `rules`: 鍙害鏉?鏂囧瓧鍦ㄦ鍐?鍜?绂佹鍏冪礌"锛屼笉闄愬埗甯冨眬

### 4.2 SKILL.md

**缁欐ā鍨嬬殑宸ヤ綔娴佹寚寮?*锛屼笉鍚‖缂栫爜绾︽潫銆傜害鏉熷叏閮ㄦ潵鑷厤缃枃浠躲€?
鍐呭瑕佺偣锛?1. 璇诲彇 `template_config.json` 鑾峰彇璁捐瑙勮寖
2. 瑙ｆ瀽鐢ㄦ埛鑷劧璇█杈撳叆锛屾彁鍙栦富棰?椤垫暟/椋庢牸/鐗规畩瑕佹眰
3. 鐢ㄦ埛鎸囧畾瑕嗙洊閰嶇疆榛樿鍊?4. 璋冪敤 `analyze_template.py` 瀵煎嚭鑳屾櫙鍥?5. 瑙勫垝椤甸潰缁撴瀯锛?-10椤碉紝鐗堝紡澶氭牱锛?6. 閫愰〉鐢熸垚 SVG锛?   - 鑳屾櫙鍥句綔涓虹涓€涓?`<image>` 鍏冪礌
   - 閬靛畧閰嶇疆涓殑 rules
   - 鍐呭瑕佷赴瀵屽叿浣?   - 甯冨眬鑷敱鍒涙柊
7. 璋冪敤 `svg_to_pptx.py` 杞崲

**SVG 鐢熸垚娉ㄦ剰浜嬮」**锛堥€氱敤锛屼笉閽堝鐗瑰畾妯″瀷锛夛細
- 杈撳嚭绾?SVG锛屼笉瑕佸姞 markdown 浠ｇ爜鍧楁爣璁?- 纭繚 XML 鏍煎紡姝ｇ‘锛堢壒娈婂瓧绗﹁浆涔夛細`<` 鈫?`&lt;`锛宍>` 鈫?`&gt;`锛?- 涓嶈鍦?SVG 浠ｇ爜鍚庨潰闄勫姞璇存槑鏂囧瓧
- viewBox 鍥哄畾涓?"0 0 1280 720"

### 4.3 analyze_template.py

**妯℃澘鍒嗘瀽鑴氭湰**锛屾敮鎸?PowerPoint 鍜?WPS銆?
鍔熻兘锛?- 鑷姩妫€娴嬫湰鏈哄畨瑁呯殑鍔炲叕杞欢锛圥owerPoint 浼樺厛锛學PS 鍏滃簳锛?- 瀵煎嚭妯℃澘姣忛〉涓?PNG 鑳屾櫙鍥撅紙1280脳720锛?- 鏍规嵁 `slides` 閰嶇疆鏄犲皠灏侀潰/鍐呭/缁撳熬

COM 鎺ュ彛锛?- PowerPoint: `comtypes.client.CreateObject("PowerPoint.Application")`
- WPS: `comtypes.client.CreateObject("KWPS.Application")` 鎴?`comtypes.client.CreateObject("PowerPoint.Application")`锛圵PS 鍏煎妯″紡锛?
### 4.4 svg_to_pptx.py

**浠?ppt-auto 鎻愬彇鐨勬牳蹇冭浆鎹㈣剼鏈?*銆?
闇€瑕佹彁鍙栫殑鏂囦欢锛?- `svg_to_pptx.py`锛堝叆鍙ｏ級
- `svg_to_pptx/` 鐩綍锛坉rawingml_converter銆乸ptx_builder 绛夛級

鍙彁鍙栬浆鎹㈢浉鍏崇殑浠ｇ爜锛屼笉鍖呭惈 ppt-auto 鐨勬ā鏉跨郴缁熴€佺‘璁?UI銆佸浘鐗囩敓鎴愮瓑銆?
### 4.5 榛樿妯℃澘

`templates/涓浗绉诲姩妯℃澘.pptx` 鎵撳寘鍐呯疆銆傜敤鎴峰湪 `template_config.json` 涓缃?`template_override` 涓烘湰鍦拌矾寰勫悗锛岄粯璁ゆā鏉垮け鏁堛€?
## 浜斻€侀厤缃鐩栨満鍒?
妯″瀷璇诲彇閰嶇疆鍚庯紝鏍规嵁鐢ㄦ埛鑷劧璇█杈撳叆瑕嗙洊锛?
| 鐢ㄦ埛璇?| 瑕嗙洊瀛楁 | 妯″瀷琛屼负 |
|---|---|---|
| "鐢╔X妯℃澘" | template_override | 浣跨敤鐢ㄦ埛鎸囧畾鐨勬ā鏉?|
| "鍋?0椤? | 椤垫暟 | 鐢熸垚10椤?|
| "娣辫壊椋庢牸" | colors | 娣辫壊閰嶈壊鏂规 |
| "鐢ㄧ豢鑹蹭富鑹? | colors.primary | 缁胯壊涓轰富 |
| "瑕佹湁琛ㄦ牸" | 甯冨眬鍋忓ソ | 鍖呭惈琛ㄦ牸椤甸潰 |
| "瀛楀彿澶т竴鐐? | 瀛楀彿 | 澧炲ぇ瀛楀彿 |

妯″瀷鐞嗚В鎰忓浘锛屼笉闇€瑕佺敤鎴峰啓 JSON銆?
## 鍏€佸疄鏂芥楠?
### Phase 1锛氭墦鍖?skill 鉁?
- [x] 鎻愬彇 `svg_to_pptx.py` 鍙婂叾渚濊禆锛堜粠 ppt-auto锛?- [x] 鎻愬彇 `svg_finalize` 妯″潡锛坰vg_to_pptx 渚濊禆锛?- [x] 鏇存柊 `analyze_template.py` 鏀寔 WPS
- [x] 绮剧畝 `template_config.json`锛堟渶灏忕害鏉燂級
- [x] 瀹屽杽 `SKILL.md`锛堝伐浣滄祦鎸囧紩 + SVG 娉ㄦ剰浜嬮」锛?- [x] 鍐呯疆 `涓浗绉诲姩妯℃澘.pptx`
- [x] 缂栧啓 `README.md`
- [x] 鍐呯疆 Python 3.12 + python-pptx + comtypes + lxml + PIL

### Phase 2锛氱鍒扮楠岃瘉 鉁?
- [x] 鐢ㄥ唴缃?Python 娴嬭瘯 analyze_template.py锛圥owerPoint COM 姝ｅ父锛?- [x] 鐢ㄥ唴缃?Python 娴嬭瘯 svg_to_pptx.py锛圫VG 杞?PPTX 鎴愬姛锛?- [x] 楠岃瘉 comtypes 杩愯鏃朵緷璧栵紙tools/ 涓嶈兘鍒犻櫎锛?- [x] 淇璺緞缂栫爜闂锛坥s.path.normpath锛?
### Phase 3锛氬彂甯冿紙寰呭畾锛?
- [ ] 闆嗘垚鍒?hiq exe 瀹夎鍖?- [ ] 娴嬭瘯鏂扮幆澧冨畨瑁?- [ ] 缂栧啓鐢ㄦ埛鏂囨。

## 涓冦€佸凡鐭ラ檺鍒?
1. **鍔炲叕杞欢渚濊禆**锛氶渶瑕?PowerPoint 鎴?WPS锛堢敤浜庢ā鏉垮垎鏋愶級
2. **瀛椾綋渚濊禆**锛氫腑鏂囨覆鏌撲緷璧栫郴缁熷瓧浣擄紙Microsoft YaHei锛?3. **骞冲彴**锛歛nalyze_template.py 浠呮敮鎸?Windows锛圕OM 鑷姩鍖栵級
4. **comtypes 棣栨杩愯**锛氭參 2-3 绉掞紙閲嶅缓 gen/ 缂撳瓨锛?
## 鍏€佸疄闄呮墦鍖呭ぇ灏?
| 缁勪欢 | 澶у皬 |
|---|---|
| Python 杩愯鏃?+ 渚濊禆 | 48MB |
| Skill 鑴氭湰 + svg_to_pptx + svg_finalize | 891KB |
| 榛樿妯℃澘 | 960KB |
| **鎬昏** | **50MB** |

渚濊禆娓呭崟锛歱ython-pptx 1.0.2銆乧omtypes銆乴xml銆丳IL/Pillow

## 涔濄€佷笉鍋氱殑浜?
1. **涓嶆墦鍖呭畬鏁?ppt-auto**鈥斺€斿彧鎻愬彇 svg_to_pptx + svg_finalize 杞崲妯″潡
2. **涓嶅湪浠ｇ爜涓‖缂栫爜甯冨眬绾︽潫**鈥斺€旂害鏉熷湪閰嶇疆鏂囦欢涓紝鐢ㄦ埛鍙敼
3. **涓嶆牎楠屾ā鍨嬭緭鍑?*鈥斺€旀ā鍨嬫槸璁捐甯堬紝鑷繁璐熻矗璐ㄩ噺
4. **涓嶇粦瀹氱壒瀹氭ā鍨?*鈥斺€攕kill 閫傜敤浜庝换浣曡兘鐢熸垚 SVG 鐨勬ā鍨?5. **涓嶈姹傜敤鎴峰畨瑁?Python**鈥斺€斿叏閮ㄥ唴缃湪 skill 鍖呬腑
