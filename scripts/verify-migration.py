"""Check the staged blog against its authorized WordPress snapshot."""
import argparse
from collections import Counter
import concurrent.futures
from html.parser import HTMLParser
import hashlib
import http.cookiejar
import json
from pathlib import Path
import re
import sqlite3
import urllib.error
import urllib.parse
import urllib.request

parser=argparse.ArgumentParser()
parser.add_argument('--base-url',default='http://127.0.0.1:8097')
parser.add_argument('--source',default='migration/wordpress.json')
parser.add_argument('--data',default='data')
args=parser.parse_args()
base=args.base_url.rstrip('/')
if urllib.parse.urlsplit(base).hostname not in ('127.0.0.1','localhost','::1'):
    raise ValueError('Verification is limited to local preview')
data=Path(args.data)
snapshot=json.loads(Path(args.source).read_text(encoding='utf-8'))
class Document(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True); self.canonical=''; self.hreflang={}; self.lang=''; self.media=[]; self.tags=[]; self.title=[]; self.in_title=False
    def handle_starttag(self,tag,attrs):
        a=dict(attrs);self.tags.append((tag,a))
        if tag=='html': self.lang=a.get('lang','')
        if tag=='title': self.in_title=True
        if tag=='link' and a.get('rel')=='canonical': self.canonical=a.get('href','')
        if tag=='link' and a.get('hreflang'): self.hreflang[a['hreflang']]=a.get('href')
        if tag in ('img','video','audio','source'):
            if a.get('src'):self.media.append(a['src'])
            if a.get('poster'):self.media.append(a['poster'])
            if a.get('srcset'): self.media.extend(p.strip().split()[0] for p in a['srcset'].split(',') if p.strip())
    def handle_endtag(self,tag):
        if tag=='title':self.in_title=False
    def handle_data(self,text):
        if self.in_title:self.title.append(text)
def fetch(path,method='GET'):
    try:
        with urllib.request.urlopen(urllib.request.Request(base+path,method=method),timeout=15) as r:return r.status,r.headers,r.read()
    except urllib.error.HTTPError as e:return e.code,e.headers,e.read()

class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self,request,fp,code,msg,headers,newurl):return None
def language_response(path,headers):
    client=urllib.request.build_opener(NoRedirect())
    try:
        with client.open(urllib.request.Request(base+path,headers=headers),timeout=15) as response:
            return response.status,response.headers,response.read()
    except urllib.error.HTTPError as error:return error.code,error.headers,error.read()
for language,accept in (('en','en-US,zh;q=0.8'),('nl','nl-NL,en;q=0.7'),('es','es-ES,en;q=0.7')):
    status,headers,_=language_response('/',{'Accept-Language':accept,'User-Agent':'Mozilla/5.0'})
    assert status==302 and headers['Location']=='/'+language+'/'
    assert headers.get('Cache-Control')=='private, no-store'
for extra in ({'Cookie':'ganwen_language=zh'},{'User-Agent':'Googlebot'}):
    status,_,body=language_response('/',{'Accept-Language':'en-US',**extra})
    assert status==200 and b'<html lang="zh-Hans">' in body
status,_,body=language_response('/?p=1875',{'Accept-Language':'es-ES','Cookie':'ganwen_language=es'})
assert status==200 and b'<html lang="zh-Hans">' in body

conn=sqlite3.connect(data/'blog.db')
posts={int(p['id']):json.loads(p['document']) for p in (dict(zip(('id','document'),row)) for row in conn.execute('SELECT id,document FROM posts'))}
assert len(posts)==len(snapshot['posts'])
for original in snapshot['posts']:
    imported=posts[int(original['id'])]
    for field in ('source_html','content_html','title','canonical_url','published_at','modified_at','status','type'):
        assert imported[field]==original[field],(original['id'],field)
    if imported['type'] in ('post','page'):
        assert set(imported['translations'])=={'en','nl','es'},(original['id'],'missing translations')
        for language,t in imported['translations'].items():
            assert t['source_hash'],(original['id'],language)
            a=Document();a.feed(original['content_html']);b=Document();b.feed(t['content_html'])
            assert a.media==b.media,(original['id'],language,'translated media changed')
            assert t['status']==('publish' if original['status']=='publish' else 'review')
            assert bool(t['title'].strip())

public=[p for p in snapshot['posts'] if p['status']=='publish' and p['type'] in ('post','page')]
checked=0;resources=set()
for p in public:
    original_url=urllib.parse.urlsplit(p['canonical_url']); route=original_url.path+'?'+original_url.query
    for language,prefix,html_lang in (('zh','','zh-Hans'),('en','/en','en'),('nl','/nl','nl'),('es','/es','es')):
        status,headers,body=fetch(prefix+route);assert status==200,(p['id'],language,status)
        assert 'noindex' in headers.get('X-Robots-Tag','')
        doc=Document();doc.feed(body.decode('utf-8'))
        canonical=('https://www.ganwen.nl'+prefix+route) if prefix else p['canonical_url']
        assert doc.canonical==canonical,(p['id'],language,doc.canonical)
        assert doc.lang==html_lang,(p['id'],language,doc.lang)
        assert set(doc.hreflang)=={'zh-Hans','en','nl','es','x-default'},(p['id'],language,doc.hreflang)
        assert doc.hreflang['zh-Hans']==p['canonical_url']
        contacts={a['data-contact-kind']:a for tag,a in doc.tags if tag=='a' and 'data-contact-kind' in a}
        assert set(contacts)=={'x','email'},(p['id'],language,'missing contact widgets')
        assert contacts['x']['href']=='https://x.com/ganwen_nl' and contacts['x'].get('target')=='_blank'
        assert contacts['email']['href']=='mailto:ganwen94@gmail.com' and 'target' not in contacts['email']
        assert all(set(a.get('rel','').split())=={'noopener','noreferrer'} for a in contacts.values())
        for media in doc.media:
            u=urllib.parse.urlsplit(media)
            if not u.netloc or u.hostname in ('ganwen.nl','www.ganwen.nl','ganwen.cn','www.ganwen.cn'):
                resources.add(urllib.parse.quote(urllib.parse.unquote(u.path),safe='/'))
        checked+=1
for p in snapshot['posts']:
    if p['status'] in ('draft','private'):
        for prefix in ('','/en','/nl','/es'):
            assert fetch(prefix+'/?p='+str(p['id']))[0]==404,(p['id'],'nonpublic leaked')
for path in ('/?p=99999999','/unknown-post/','/bewijs.html','/wp-config.php'):
    assert fetch(path)[0]==404,path
for path in ('/?sitemap=index','/?sitemap=posts-post-1','/?feed=rss2','/robots.txt'):
    status,headers,body=fetch(path);assert status==200,(path,status)
    if 'robots' not in path:assert body.startswith(b'<?xml'),path
def media_check(path):
    status,_,_=fetch(path,'HEAD'); return path,status
missing=[r for r in concurrent.futures.ThreadPoolExecutor(max_workers=8).map(media_check,sorted(resources)) if r[1]!=200]
assert not missing,('referenced media missing',missing)
manifest=json.loads(Path('migration/media-manifest.json').read_text(encoding='utf-8'))
for m in manifest:
    file=data/'media'/m['path'];raw=file.read_bytes()
    assert len(raw)==m['bytes'] and hashlib.sha256(raw).hexdigest()==m['sha256'],m['path']
aliases=json.loads(Path('scripts/media-aliases.json').read_text(encoding='utf-8'))
for alias in aliases:
    assert hashlib.sha256((data/'media'/alias['path']).read_bytes()).hexdigest()==alias['sha256']

# Test the generated administrator login without displaying credentials.
jar=http.cookiejar.CookieJar();client=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
session=json.loads(client.open(base+'/admin/api/session').read())
status,headers,login_body=fetch('/admin/')
assert status==200 and 'noindex' in headers.get('X-Robots-Tag','')
login_html=login_body.decode('utf-8')
for unwanted in ('使用服务器配置的管理员账号','后台内容不被搜索引擎收录'):
    assert unwanted not in login_html,unwanted
credentials=(data/'admin-access.txt').read_text(encoding='utf-8').splitlines()
user=credentials[0].split('：',1)[1];password=credentials[1].split('：',1)[1]
request=urllib.request.Request(base+'/admin/api/login',data=json.dumps({'username':user,'password':password}).encode('utf-8'),headers={'Content-Type':'application/json','Origin':base,'X-CSRF-Token':session['csrf']})
logged=json.loads(client.open(request).read());assert logged['csrf']
articles=json.loads(client.open(base+'/admin/api/posts').read());assert len(articles)==44
config=json.loads(client.open(base+'/admin/api/translation-config').read());assert not config.get('api_key')
request=urllib.request.Request(base+'/admin/api/logout',data=b'{}',headers={'Content-Type':'application/json','Origin':base,'X-CSRF-Token':logged['csrf']})
assert client.open(request).status==200
summary={'database_records':len(posts),'articles_and_pages':44,'translations':sum(len(p.get('translations',{})) for p in posts.values()),'public_language_pages':checked,'verified_media_files':len(manifest),'missing_original_thumbnail_aliases':len(aliases),'referenced_media_urls':len(resources),'legacy_public_urls':len(public),'admin_login_logout':'passed','public_contact_widgets':'x,email','minimal_login_page':'passed','homepage_language_detection':'passed','private_draft_originals_unchanged':True}
Path('migration/verification.json').write_text(json.dumps(summary,ensure_ascii=False,indent=2),encoding='utf-8')
print(json.dumps(summary,ensure_ascii=False))
