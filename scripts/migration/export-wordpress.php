<?php
// CLI only. Uses WordPress's existing connection; never exports credentials.
if (PHP_SAPI !== 'cli') { http_response_code(404); exit; }
define('DISABLE_WP_CRON', true);
define('WP_USE_THEMES', false);
$root = $argv[1] ?? '/www/wwwroot/ganwen.nl';
$destination = $argv[2] ?? '/tmp/ganwen-go-migration-20261007';
if (!is_dir($destination) && !mkdir($destination,0700,true)) { throw new RuntimeException('Cannot create export directory'); }
require $root.'/wp-load.php';
global $wpdb;
function gw_terms($id, $taxonomy) {
    $terms = get_the_terms($id,$taxonomy); $out=[];
    if (is_array($terms)) foreach ($terms as $t) $out[]=['id'=>$t->term_id,'name'=>$t->name,'slug'=>$t->slug,'url'=>get_term_link($t),'taxonomy'=>$taxonomy];
    return $out;
}
function gw_all_terms($taxonomy) {
    $out=[]; foreach (get_terms(['taxonomy'=>$taxonomy,'hide_empty'=>false]) as $t) $out[]=['id'=>$t->term_id,'name'=>$t->name,'slug'=>$t->slug,'url'=>get_term_link($t),'taxonomy'=>$taxonomy]; return $out;
}
function gw_date($value) { return (new DateTime($value,wp_timezone()))->format(DateTime::ATOM); }
$posts=[]; $refs=[]; $media=[];
$rows=$wpdb->get_results("SELECT * FROM {$wpdb->posts} WHERE post_type IN ('post','page','attachment') AND post_status <> 'auto-draft' ORDER BY ID");
foreach ($rows as $row) {
    $p=new WP_Post($row); $GLOBALS['post']=$p; setup_postdata($p);
    $status=$p->post_status;
    if ($p->post_type==='attachment') { $parent=$p->post_parent?get_post($p->post_parent):null; $status=(!$parent||$parent->post_status==='publish')?'publish':'private'; }
    $rendered=apply_filters('the_content',$p->post_content);
    $author=get_userdata($p->post_author);
    $post=['id'=>(int)$p->ID,'version'=>1,'type'=>$p->post_type,'status'=>$status,'slug'=>$p->post_name,'title'=>$p->post_title,'source_html'=>$p->post_content,'content_html'=>$rendered,'excerpt_html'=>$p->post_excerpt,'canonical_url'=>$p->post_type==='attachment'?get_attachment_link($p->ID):get_permalink($p->ID),'published_at'=>gw_date($p->post_date),'modified_at'=>gw_date($p->post_modified),'author'=>$author?$author->display_name:'甘','author_id'=>(int)$p->post_author,'categories'=>gw_terms($p->ID,'category'),'tags'=>gw_terms($p->ID,'post_tag'),'format'=>get_post_format($p->ID)?:'','featured_image'=>get_the_post_thumbnail_url($p->ID,'full')?:'','sticky'=>is_sticky($p->ID),'seo'=>['title'=>'','description'=>''],'translations'=>(object)[]];
    $posts[]=$post;
    $refs[]=$p->post_content; $refs[]=$rendered; $refs[]=$post['featured_image'];
    if ($p->post_type==='attachment') { $file=get_attached_file($p->ID); $media[]=['id'=>$p->ID,'url'=>wp_get_attachment_url($p->ID),'file'=>str_replace($root.'/','',$file?:''),'mime'=>$p->post_mime_type,'parent'=>$p->post_parent]; }
}
$header=get_theme_mod('header_image','');$refs[]=$header;
$revisions=$wpdb->get_results("SELECT ID,post_parent,post_title,post_content,post_excerpt,post_date,post_date_gmt,post_modified,post_modified_gmt FROM {$wpdb->posts} WHERE post_type='revision' ORDER BY ID",ARRAY_A);
$export=['settings'=>['title'=>get_option('blogname'),'description'=>get_option('blogdescription'),'base_url'=>get_option('home'),'posts_per_page'=>(int)get_option('posts_per_page'),'categories'=>gw_all_terms('category'),'tags'=>gw_all_terms('post_tag'),'header_image'=>$header],'posts'=>$posts,'revisions'=>$revisions,'media'=>$media];
$json=json_encode($export,JSON_UNESCAPED_SLASHES|JSON_UNESCAPED_UNICODE|JSON_THROW_ON_ERROR);
file_put_contents($destination.'/wordpress.json',$json);chmod($destination.'/wordpress.json',0600);
// Preserve every upload, including originals, responsive sizes, and unregistered files.
$paths=[];
$it=new RecursiveIteratorIterator(new RecursiveDirectoryIterator($root.'/wp-content/uploads',FilesystemIterator::SKIP_DOTS));
foreach($it as $f) if($f->isFile()&&!$f->isLink()) $paths[]=substr($f->getPathname(),strlen($root)+1);
// Include root media referenced by blog content, never standalone evidence HTML or PHP.
$all=implode("\n",$refs);
foreach(glob($root.'/*') as $file) if(is_file($file)&&preg_match('/\.(jpe?g|png|gif|webp|avif|ico|mp4|mov|m4v|webm|mp3|m4a|wav|ogg|pdf|heic|heif)$/i',$file)) { $name=basename($file); if(strpos($all,$name)!==false||$name==='favicon.gif') $paths[]=$name; }
$paths=array_values(array_unique($paths));sort($paths);
file_put_contents($destination.'/media-paths.txt',implode("\n",$paths)."\n");chmod($destination.'/media-paths.txt',0600);
$manifest=[];foreach($paths as $path) $manifest[]=['path'=>$path,'bytes'=>filesize($root.'/'.$path),'sha256'=>hash_file('sha256',$root.'/'.$path)];
file_put_contents($destination.'/media-manifest.json',json_encode($manifest,JSON_UNESCAPED_SLASHES|JSON_UNESCAPED_UNICODE|JSON_THROW_ON_ERROR));chmod($destination.'/media-manifest.json',0600);
$counts=[];foreach($posts as $p){$key=$p['type'].':'.$p['status'];$counts[$key]=($counts[$key]??0)+1;}
echo json_encode(['counts'=>$counts,'revisions'=>count($revisions),'media_files'=>count($manifest),'media_bytes'=>array_sum(array_column($manifest,'bytes')),'export_sha256'=>hash('sha256',$json)],JSON_UNESCAPED_UNICODE|JSON_PRETTY_PRINT)."\n";
