// 생성 중인 카드가 있을 때만 폴링한다. 없으면 아무것도 하지 않는다.
if (document.getElementById('poll')) {
  setTimeout(function () { location.reload(); }, 5000);
}

// 편집 화면: 글자 수 카운터와 미리보기를 입력에 맞춰 갱신한다.
var body = document.getElementById('body');
if (body) {
  var counter = document.getElementById('counter');
  var previewBody = document.getElementById('preview-body');
  var room = parseInt(counter.dataset.room, 10);

  var update = function () {
    var text = body.value.trim();
    // 한글은 바이트로 세면 3배가 되므로 코드 포인트 단위로 센다.
    var used = Array.from(text).length;
    counter.textContent = used + ' / ' + room;
    counter.classList.toggle('is-over', used > room);
    previewBody.textContent = text;
  };

  body.addEventListener('input', update);
  update();
}
