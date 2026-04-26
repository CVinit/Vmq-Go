package app

type CommonRes struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data,omitempty"`
}

type PageRes struct {
	Code  int    `json:"code"`
	Msg   string `json:"msg"`
	Count int64  `json:"count"`
	Data  any    `json:"data,omitempty"`
}

type CreateOrderRes struct {
	PayID       string  `json:"payId"`
	OrderID     string  `json:"orderId"`
	AccessToken string  `json:"accessToken,omitempty"`
	PayType     int     `json:"payType"`
	Price       float64 `json:"price"`
	ReallyPrice float64 `json:"reallyPrice"`
	PayURL      string  `json:"payUrl"`
	IsAuto      int     `json:"isAuto"`
	State       int     `json:"state"`
	TimeOut     int     `json:"timeOut"`
	Date        int64   `json:"date"`
}

type PayOrder struct {
	ID          int64   `json:"id"`
	OrderID     string  `json:"orderId"`
	PayID       string  `json:"payId"`
	CreateDate  int64   `json:"createDate"`
	PayDate     int64   `json:"payDate"`
	CloseDate   int64   `json:"closeDate"`
	Param       string  `json:"param"`
	Type        int     `json:"type"`
	Price       float64 `json:"price"`
	ReallyPrice float64 `json:"reallyPrice"`
	NotifyURL   string  `json:"notifyUrl"`
	ReturnURL   string  `json:"returnUrl"`
	State       int     `json:"state"`
	IsAuto      int     `json:"isAuto"`
	PayURL      string  `json:"payUrl"`
}

type PayQRCode struct {
	ID     int64   `json:"id"`
	PayURL string  `json:"payUrl"`
	Price  float64 `json:"price"`
	Type   int     `json:"type"`
}

type DashboardStats struct {
	TodayOrder        int64
	TodaySuccessOrder int64
	TodayCloseOrder   int64
	TodayMoney        float64
	CountOrder        int64
	CountMoney        float64
}

type OrderFilter struct {
	Type  *int
	State *int
}

func successRes(data any) CommonRes {
	return CommonRes{Code: 1, Msg: "成功", Data: data}
}

func successOnly() CommonRes {
	return CommonRes{Code: 1, Msg: "成功"}
}

func errorRes(msg string) CommonRes {
	return CommonRes{Code: -1, Msg: msg}
}

func errorOnly() CommonRes {
	return CommonRes{Code: -1, Msg: "失败"}
}

func errorResCode(code int, data any) CommonRes {
	return CommonRes{Code: code, Msg: "失败", Data: data}
}

func pageSuccess(count int64, data any) PageRes {
	return PageRes{Code: 0, Msg: "成功", Count: count, Data: data}
}

func pageError(msg string) PageRes {
	return PageRes{Code: -1, Msg: msg, Count: 0}
}
