const {chromium}=require('playwright');
const assert=require('node:assert/strict');
(async()=>{
 const browser=await chromium.launch({headless:true,args:['--no-sandbox']});
 try{
 const context=await browser.newContext();const base=process.argv[2];
 await context.addCookies([{name:'awsportal_session',value:'browser-session',url:base}]);
 const page=await context.newPage();const errors=[],external=[];let documents=0,requests=0;
 page.on('pageerror',e=>errors.push(e.message));page.on('request',r=>{requests++;if(r.isNavigationRequest())documents++;if(!r.url().startsWith(base))external.push(r.url())});
 await page.goto(base);await page.locator('nav a[href="/instances"]').click();await page.waitForURL(base+'/instances');
 await page.locator('#instance-filter').fill('ADAS');assert.equal(await page.locator('tr[data-instance]:visible').count(),1);
 await Promise.all([page.waitForResponse(r=>r.url()===base+'/instances'&&r.request().headers()['hx-target']==='instances-live'),page.getByRole('button',{name:'更新'}).click()]);
 await page.waitForTimeout(100);assert.equal(await page.locator('#instances-live').count(),1);assert.equal(await page.locator('#instance-filter').inputValue(),'ADAS');assert.equal(await page.locator('tr[data-instance]:visible').count(),1);
 await page.locator('#instance-filter').fill('no-match');assert.equal(await page.locator('#instance-empty:visible').count(),1);
 await page.locator('#instance-filter').fill('');
 await page.locator('tr[data-instance="i-dev"] a', {hasText:'詳細'}).click();await page.waitForURL(base+'/instances/i-dev');
 await page.getByRole('button',{name:'起動',exact:true}).click();await page.waitForSelector('#instance-live [action$="/stop"]');
 assert.equal(await page.locator('#instance-live [data-transition]').count(),0);
 await page.waitForTimeout(100);const idle=requests;await page.waitForTimeout(1800);assert.equal(requests,idle,'stable state must not poll');
 await page.locator('nav a[href="/instances"]').click();await page.waitForURL(base+'/instances');
 await page.locator('#state-filter').selectOption('running');assert.equal(await page.locator('tr[data-instance]:visible').count(),2);
 await page.locator('nav a[href="/"]').click();await page.waitForURL(base+'/');await page.locator('nav a[href="/instances"]').click();await page.waitForURL(base+'/instances');
 await page.locator('#instance-filter').fill('CV');assert.equal(await page.locator('tr[data-instance]:visible').count(),1);
 // Server failures stay visible without replacing the table or executing response markup.
 await page.route('**/instances',route=>route.request().headers()['hx-target']==='instances-live'?route.fulfill({status:502,contentType:'text/plain',body:'AWS状態を取得できません'}):route.continue());
 await page.getByRole('button',{name:'更新'}).click();await page.waitForSelector('#request-notice');assert.match(await page.locator('#request-notice').textContent(),/AWS/);assert.equal(await page.locator('tr[data-instance]').count(),2);await page.unroute('**/instances');
 await page.locator('nav a[href="/costs"]').click();await page.waitForURL(base+'/costs');
 await page.locator('input[name="month"]').fill('2026-09');await page.getByRole('button',{name:'表示',exact:true}).click();await page.waitForURL(base+'/costs?month=2026-09');assert.equal(await page.locator('input[name="month"]').inputValue(),'2026-09');
 await page.locator('nav a[href="/instances"]').click();await page.waitForURL(base+'/instances');await page.evaluate(()=>document.fonts.ready);
 for(const [width,height] of [[1366,768],[1920,1080],[2560,1440],[3840,2160]]){
  await page.setViewportSize({width,height});await page.locator('#instance-filter').fill('');
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true,`page overflow at ${width}`);
  assert.equal(await page.locator('aside').count(),1);
  if(process.env.AWSPORTAL_SCREENSHOT_DIR)await page.screenshot({path:`${process.env.AWSPORTAL_SCREENSHOT_DIR}/instances-${width}x${height}.png`});
 }

 await page.locator('nav a[href="/admin/instances"]').click();await page.waitForURL(base+'/admin/instances');
 const card=page.locator('[data-admin-instance="i-dev"]');
 const userForm=card.locator('form').filter({has:page.locator('input[value="user"]')}).filter({has:page.locator('input[value="assign"]')});
 await userForm.locator('select[name="subject_id"]').selectOption({label:'alice'});await userForm.getByRole('button',{name:'ユーザー割り当て'}).click();await page.waitForSelector('[data-admin-instance="i-dev"] .assignment-list strong');
 const userContext=await browser.newContext();await userContext.addCookies([{name:'awsportal_session',value:'alice-session',url:base}]);const userPage=await userContext.newPage();await userPage.goto(base+'/instances');assert.equal(await userPage.locator('tr[data-instance]').count(),1);assert.equal(await userPage.getByRole('button',{name:'停止',exact:true}).count(),0);assert.equal(await userPage.locator('nav a[href="/admin/instances"]').count(),0);
 await page.locator('form').filter({has:page.locator('input[value="create-group"]')}).locator('input[name="name"]').fill('ADAS Team');await page.getByRole('button',{name:'グループ作成',exact:true}).click();await page.waitForSelector('select[name="group_id"] option:text("ADAS Team")',{state:'attached'});
 const memberForm=page.locator('form').filter({has:page.locator('input[value="add-member"]')});await memberForm.locator('select[name="group_id"]').selectOption({label:'ADAS Team'});await memberForm.locator('select[name="user_id"]').selectOption({label:'alice'});await memberForm.getByRole('button',{name:'メンバー追加'}).click();await page.waitForSelector('.group-members form');
 const groupForm=card.locator('form').filter({has:page.locator('input[value="group"]')}).filter({has:page.locator('input[value="assign"]')});await groupForm.locator('select[name="subject_id"]').selectOption({label:'ADAS Team'});await groupForm.locator('select[name="permission"]').selectOption('control');await groupForm.getByRole('button',{name:'グループ割り当て'}).click();await page.waitForSelector('[data-admin-instance="i-dev"] .assignment-list form:nth-child(2)');
 await userPage.reload();assert.equal(await userPage.locator('tr[data-instance]').count(),1);assert.equal(await userPage.getByRole('button',{name:'停止',exact:true}).count(),1);
 await card.getByRole('button',{name:'無効化',exact:true}).click();await card.getByRole('button',{name:'再有効化',exact:true}).waitFor();await userPage.reload();assert.equal(await userPage.locator('tr[data-instance]').count(),0);
 await card.getByRole('button',{name:'再有効化',exact:true}).click();await card.getByRole('button',{name:'無効化',exact:true}).waitFor();
 for(const [width,height] of [[1366,768],[1920,1080],[2560,1440],[3840,2160],[390,844]]){await page.setViewportSize({width,height});assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true,`admin page overflow at ${width}`);assert.equal(await page.getByRole('button',{name:'Logout',exact:true}).isVisible(),true);if(process.env.AWSPORTAL_SCREENSHOT_DIR)await page.screenshot({path:`${process.env.AWSPORTAL_SCREENSHOT_DIR}/instance-admin-${width}x${height}.png`})}
 await page.locator('nav a[href="/admin/proxy"]').click();await page.waitForURL(base+'/admin/proxy');
 const addRule=page.locator('section').filter({has:page.getByRole('heading',{name:'ルール追加',exact:true})});
 await addRule.locator('[name="name"]').fill('GitHub');await addRule.locator('[name="domain"]').fill('github.com');await addRule.getByRole('button',{name:'保存',exact:true}).click();await page.getByRole('heading',{name:'GitHub'}).waitFor();assert.equal(await page.locator('#proxy-live').count(),1);
 const savedRule=page.locator('section').filter({has:page.getByRole('heading',{name:'GitHub'})});await savedRule.locator('[name="effect"]').selectOption('deny');await savedRule.getByRole('button',{name:'保存',exact:true}).click();await page.getByRole('heading',{name:'GitHub deny'}).waitFor();
 const issue=page.locator('form').filter({has:page.locator('input[value="issue"]')});await issue.locator('[name="subject_id"]').selectOption({label:'alice'});await issue.locator('[name="label"]').fill('build');await issue.getByRole('button',{name:'発行',exact:true}).click();await page.locator('.proxy-secret').waitFor();assert.match(await page.locator('.proxy-secret').textContent(),/^[a-f0-9]{64}$/);
 await page.getByRole('button',{name:'失効',exact:true}).click();await page.locator('.proxy-secret').waitFor({state:'detached'});assert.equal(await page.getByRole('button',{name:'失効',exact:true}).count(),0);
 for(const [width,height] of [[1366,768],[1920,1080],[2560,1440],[3840,2160],[390,844]]){await page.setViewportSize({width,height});assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true,`proxy page overflow at ${width}`);if(process.env.AWSPORTAL_SCREENSHOT_DIR)await page.screenshot({path:`${process.env.AWSPORTAL_SCREENSHOT_DIR}/proxy-${width}x${height}.png`,fullPage:true})}
 await savedRule.getByRole('button',{name:'ルール削除',exact:true}).click();await page.getByRole('heading',{name:'GitHub'}).waitFor({state:'detached'});
 await page.locator('nav a[href="/admin/egress"]').click();await page.waitForURL(base+'/admin/egress');
 const outbound=page.locator('[data-egress-instance="i-dev"]');await outbound.locator('[name="security_group_id"]').fill('sg-dev');await outbound.locator('[name="proxy_group_id"]').fill('sg-proxy');await outbound.locator('[name="rules"]').fill('10.20.0.0/16 tcp 443\n2001:db8::/64 udp 123');await outbound.getByRole('button',{name:'保存（未適用）',exact:true}).click();await outbound.getByRole('button',{name:'AWS状態確認',exact:true}).waitFor();assert.equal(await page.locator('#egress-live').count(),1);assert.match(await outbound.textContent(),/保存版 1 \/ 最終適用版 0/);
 await outbound.getByRole('button',{name:'保存版をAWSへ適用',exact:true}).click();await page.getByRole('status').filter({hasText:'設定一致を確認'}).waitFor();assert.match(await outbound.textContent(),/最終適用版 1/);
 for(const [width,height] of [[1366,768],[1920,1080],[2560,1440],[3840,2160],[390,844]]){await page.setViewportSize({width,height});assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true,`egress overflow at ${width}`);if(process.env.AWSPORTAL_SCREENSHOT_DIR)await page.screenshot({path:`${process.env.AWSPORTAL_SCREENSHOT_DIR}/egress-${width}x${height}.png`,fullPage:true})}
 await page.locator('nav a[href="/mirrors"]').click();await page.waitForURL(base+'/mirrors');
 const createMirror=page.locator('section').filter({has:page.getByRole('heading',{name:'ミラー追加',exact:true})});await createMirror.locator('[name="name"]').fill('Team Code');await createMirror.locator('[name="upstream"]').fill('https://gitlab.example/team/code.git');await createMirror.getByRole('button',{name:'追加',exact:true}).click();await page.locator('[data-mirror-id="1"]').waitFor();
 const mirrorCard=page.locator('[data-mirror-id="1"]');const mirrorGrant=mirrorCard.locator('form').filter({has:page.locator('[value="grant"]')});await mirrorGrant.locator('[name="target"]').selectOption({label:'ユーザー: alice'});await mirrorGrant.locator('[name="can_sync"]').selectOption('1');await mirrorGrant.getByRole('button',{name:'割り当て',exact:true}).click();await page.getByText('repo 1 / alice / 読み取り＋同期',{exact:true}).waitFor();await mirrorCard.getByRole('button',{name:'同期を要求',exact:true}).click();await page.getByText(/#1 \/ repo 1 \/.*queued/).waitFor();assert.equal(await page.locator('#mirrors-live').count(),1);
 for(const [width,height] of [[1366,768],[1920,1080],[2560,1440],[3840,2160],[390,844]]){await page.setViewportSize({width,height});assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true,`mirrors overflow at ${width}`);if(process.env.AWSPORTAL_SCREENSHOT_DIR)await page.screenshot({path:`${process.env.AWSPORTAL_SCREENSHOT_DIR}/mirrors-${width}x${height}.png`,fullPage:true})}
 // Manuals are available to users, and branding changes are administrator-only.
 await page.locator('nav a[href="/manual"]').click();await page.waitForURL(base+'/manual');
 assert.equal(await page.getByRole('heading',{name:'プロキシの設定方法',exact:true}).count(),1);
 assert.equal(await page.getByRole('heading',{name:'Gitミラーの利用方法',exact:true}).count(),1);
 assert.match(await page.locator('#git-guide').textContent(),/awsportal-mirror sync 1 --wait/);
 for(const [width,height] of [[1366,768],[1920,1080],[2560,1440],[3840,2160],[390,844]]){await page.setViewportSize({width,height});assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true,`manual overflow at ${width}`);if(process.env.AWSPORTAL_SCREENSHOT_DIR)await page.screenshot({path:`${process.env.AWSPORTAL_SCREENSHOT_DIR}/manual-${width}x${height}.png`})}
 await userPage.locator('nav a[href="/manual"]').click();await userPage.waitForURL(base+'/manual');assert.equal(await userPage.locator('#git-guide').count(),1);assert.equal(await userPage.locator('nav a[href="/admin/site"]').count(),0);
 assert.equal(documents,1,'boosted navigation must not reload document');
 await page.setViewportSize({width:1366,height:768});await page.locator('nav a[href="/admin/site"]').click();await page.waitForURL(base+'/admin/site');
 const settings=page.locator('.site-settings-form');
 const brand='研究開発ポータル';await settings.locator('[name="brand_title"]').fill(brand);await settings.locator('[name="brand_subtitle"]').fill('社内研究環境\n利用者向け');await settings.locator('[name="home_title"]').fill('研究環境ホーム');await settings.locator('[name="home_message"]').fill('利用前にマニュアルを確認してください。\nメンテナンス予定：毎週土曜');await settings.locator('[name="login_message"]').fill('研究環境へログイン');await settings.locator('[name="help_message"]').fill('接続で困ったら研究環境の管理者へ');await settings.locator('[name="portal_url"]').fill('https://portal.company.example');await settings.locator('[name="proxy_url"]').fill('https://proxy.company.example:3128');
 await settings.getByRole('button',{name:'表示設定を保存',exact:true}).click();await page.waitForURL(base+'/admin/site?saved=1');await page.getByRole('status').filter({hasText:'保存しました'}).waitFor();assert.equal(await page.locator('.brand strong').textContent(),brand);
 const afterSaveDocuments=documents;
 for(const [width,height] of [[1366,768],[1920,1080],[2560,1440],[3840,2160],[390,844]]){await page.setViewportSize({width,height});assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true,`settings overflow at ${width}`);if(width===390)assert.ok((await page.locator("aside").boundingBox()).height<230,"mobile navigation must remain compact");if(process.env.AWSPORTAL_SCREENSHOT_DIR)await page.screenshot({path:`${process.env.AWSPORTAL_SCREENSHOT_DIR}/site-settings-${width}x${height}.png`})}
 await page.locator('nav a[href="/"]').click();await page.waitForURL(base+'/');assert.equal(await page.locator('h1').textContent(),'研究環境ホーム');assert.match(await page.locator('.home-message').textContent(),/メンテナンス予定/);
 await userPage.locator('nav a[href="/"]').click();await userPage.waitForURL(base+'/');assert.equal(await userPage.locator('.brand strong').textContent(),brand);assert.equal(await userPage.locator('h1').textContent(),'研究環境ホーム');
 await page.locator('nav a[href="/manual"]').click();await page.waitForURL(base+'/manual');assert.match(await page.locator('#proxy-guide').textContent(),/https:\/\/proxy.company.example:3128/);assert.match(await page.locator('#git-guide').textContent(),/https:\/\/portal.company.example/);assert.match(await page.locator('.manual-content').textContent(),/研究環境の管理者/);
 assert.equal(documents,afterSaveDocuments,'navigation after save must remain boosted');
 // Job history has owner boundaries and stays usable at desktop/mobile widths.
 await userPage.goto(base+'/environments');
 assert.equal(await userPage.getByText('a'.repeat(32),{exact:true}).count(),0);
 assert.equal(await userPage.getByText('b'.repeat(32),{exact:true}).count(),1);
 for(const path of ['/environments','/admin/environments']){
  await page.goto(base+path);
  assert.equal(await page.getByText('a'.repeat(32),{exact:true}).count(),1);
  assert.equal(await page.getByText('b'.repeat(32),{exact:true}).count(),1);
  assert.match(await page.locator('main').textContent(),/計測が古い/);
  for(const [width,height] of [[1366,768],[1920,1080],[2560,1440],[3840,2160],[390,844]]){
   await page.setViewportSize({width,height});
   assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true,`job history overflow at ${width} (${path})`);
   if(process.env.AWSPORTAL_SCREENSHOT_DIR)await page.screenshot({path:`${process.env.AWSPORTAL_SCREENSHOT_DIR}/jobs-${path.includes('admin')?'admin':'user'}-${width}x${height}.png`,fullPage:true});
  }
 }
 await userContext.close();
 assert.deepEqual(errors,[]);assert.deepEqual(external,[]);
 console.log('PASS: boosted navigation, repeated search, partial refresh, detail action/poll termination, idle traffic, errors, cost month/history URL, four desktop resolutions plus mobile logout, administrator-only instance disable/reactivate, user/group grants and effective user access, no external requests');
 }finally{await browser.close()}
})().catch(e=>{console.error(e);process.exit(1)});
