// 생성 중인 카드가 있을 때만 폴링한다. 없으면 아무것도 하지 않는다.
if (document.getElementById('poll')) {
  setTimeout(function () { location.reload(); }, 5000);
}

// 편집 화면: 글자 수 카운터와 미리보기를 입력에 맞춰 갱신한다.
function bindCounter(fieldId, counterId, previewId, cardId) {
  var field = document.getElementById(fieldId);
  if (!field) return;

  var counter = document.getElementById(counterId);
  var preview = document.getElementById(previewId);
  var card = cardId ? document.getElementById(cardId) : null;
  var room = parseInt(counter.dataset.room, 10);

  var update = function () {
    var text = field.value.trim();
    // 한글은 바이트로 세면 3배가 되므로 코드 포인트 단위로 센다.
    var used = Array.from(text).length;
    counter.textContent = used + ' / ' + room;
    counter.classList.toggle('is-over', used > room);
    if (preview) preview.textContent = text;
    // 답글 카드는 내용이 있을 때만 보여준다.
    if (card) card.hidden = text === '';
  };

  field.addEventListener('input', update);
  update();
}

bindCounter('body', 'counter', 'preview-body', null);
bindCounter('detail', 'detail-counter', 'preview-detail', 'preview-detail-card');

// 발행 버튼은 네트워크가 느리면 두 번 눌리기 쉽다. 첫 제출에 잠근다.
// 서버도 조건부 UPDATE로 막지만, 버튼이 계속 눌리는 화면은 불안하다.
document.querySelectorAll('[data-publish]').forEach(function (form) {
  form.addEventListener('submit', function () {
    var btn = form.querySelector('button');
    btn.disabled = true;
    btn.textContent = '발행 중...';
  });
});
