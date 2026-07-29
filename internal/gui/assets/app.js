(function(){
'use strict';

// Disable right-click context menu
document.addEventListener('contextmenu',function(e){e.preventDefault()});

function showScreen(s){var sc=document.querySelectorAll('.screen');for(var i=0;i<sc.length;i++)sc[i].classList.toggle('hidden',sc[i].id!=='screen-'+s)}
function setText(id,t){var e=document.getElementById(id);if(e)e.textContent=t}
function setWidth(id,p){var e=document.getElementById(id);if(e)e.style.width=p+'%'}
function fmtSp(m){if(m<=0)return '— MB/s';if(m<1)return Math.round(m*1024)+' KB/s';return m.toFixed(1)+' MB/s'}
function fmtEta(s){if(s<=0)return 'осталось —';if(s<60)return 'осталось '+s+' с';var m=Math.floor(s/60),r=s%60;return 'осталось '+m+':'+(r<10?'0':'')+r}
function fmtSz(m){if(m<1)return Math.round(m*1024)+' KB';return m.toFixed(1)+' MB'}
window.__updateState=function(p){
  showScreen(p.state);
  if(p.version){setText('dl-version',p.version);setText('ua-version',p.version);setText('fu-version',p.version)}
  if(p.title)setText('welcome-path',p.title);
  if(p.state==='installed'){setText('inst-version','Версия '+p.version);setText('inst-path',p.title)}
  if(p.state==='downloading')setText('dl-title',p.isFirstRun?'Установка Syntee Helper':'Загрузка обновления');
  if(p.changelog!==undefined)setText('ua-changelog',p.changelog||'');
  if(p.errorMessage)setText('error-msg',p.errorMessage);
  if(p.title&&p.state==='done'){setText('done-title',p.title);setText('done-detail',p.detail||'')}
  if(p.progress){var pr=p.progress,pct=pr.percent||0;
    if(p.state==='downloading'){setWidth('dl-fill',pct);setText('dl-percent',pct+'%');setText('dl-speed',fmtSp(pr.speedMBps));setText('dl-size',fmtSz(pr.downloadedMB)+' / '+fmtSz(pr.totalMB));setText('dl-eta',fmtEta(pr.etaSec))}
    else if(p.state==='extracting'){setWidth('ex-fill',pct);setText('ex-percent',pct+'%')}
    else if(p.state==='forced_update'){setWidth('fu-fill',pct);setText('fu-percent',pct+'%');setText('fu-speed',fmtSp(pr.speedMBps));setText('fu-size',fmtSz(pr.downloadedMB)+' / '+fmtSz(pr.totalMB));setText('fu-eta',fmtEta(pr.etaSec))}
  }
};
window.__setCountdown=function(sec){
  var e=document.getElementById('launch-countdown');
  if(e)e.textContent='Окно закроется через '+sec;
};
document.getElementById('btn-min').addEventListener('click',function(){window.minimize()});
document.getElementById('btn-close').addEventListener('click',function(){window.closeWindow()});
document.getElementById('btn-update-now').addEventListener('click',function(){window.updateNow()});
document.getElementById('btn-later').addEventListener('click',function(){window.later()});
document.getElementById('btn-retry').addEventListener('click',function(){window.retry()});
document.getElementById('btn-done-close').addEventListener('click',function(){window.closeWindow()});
document.getElementById('btn-install').addEventListener('click',function(){
  var opts={
    desktopShortcut:document.getElementById('opt-desktop').checked,
    startMenuShortcut:document.getElementById('opt-startmenu').checked,
    autostart:document.getElementById('opt-autostart').checked
  };
  window.startInstall(opts);
});
document.getElementById('btn-installed-close').addEventListener('click',function(){window.closeWindow()});
document.getElementById('btn-reinstall').addEventListener('click',function(){window.reinstall()});
document.getElementById('btn-launch-client').addEventListener('click',function(){window.launchClient()});
document.getElementById('btn-verify').addEventListener('click',function(){
  var r=document.getElementById('inst-verify');
  r.classList.remove('show','err');
  r.textContent='Проверка…';
  r.classList.add('show');
  window.verifyIntegrity().then(function(res){
    r.textContent=res.message;
    if(!res.ok)r.classList.add('err');
  });
});
var tb=document.getElementById('titlebar');
tb.addEventListener('mousedown',function(e){if(e.target.closest('.titlebar-btn'))return;window.startDrag()});
})();
