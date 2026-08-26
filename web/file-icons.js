(() => {
  'use strict';

  const extensions = new Set([
    '7z','aac','apk','appx','avi','bat','bmp','bz2','c','cab','cc','cmd','com','cpp','css','csv',
    'dmg','doc','docm','docx','eot','exe','flac','flv','gif','go','gz','h','heic','html','ico','img',
    'iso','java','jpeg','jpg','js','json','jsx','m4a','m4v','md','mkv','mov','mp3','mp4','mpeg','mpg',
    'msi','odp','ods','odt','ogg','opus','otf','pdf','php','png','ppt','pptm','pptx','ps1','py','rar',
    'raw','rb','rs','rtf','scss','sh','sql','svg','tar','tgz','tif','tiff','ts','tsx','ttf','txt','vhd',
    'vhdx','vue','wav','webm','webp','wma','wmv','woff','woff2','xls','xlsb','xlsm','xlsx','xml','xz',
    'yaml','yml','zip'
  ]);

  function extension(name) {
    const match = String(name || '').toLowerCase().match(/\.([^.\\/]+)$/);
    return match ? match[1] : '';
  }

  function source(type, name) {
    if (type === 'folder') return '/assets/system-icon-folder.png';
    const ext = extension(name);
    return `/assets/system-icon-${extensions.has(ext) ? ext : 'file'}.png`;
  }

  function markup(type, name) {
    return `<img class="es-system-icon" src="${source(type, name)}" alt="" draggable="false">`;
  }

  window.EverythingShareIcons = Object.freeze({ extension, source, markup });
})();
